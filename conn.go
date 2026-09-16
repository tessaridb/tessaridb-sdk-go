package tessaridb

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"time"
)

// Credentials are presented once per connection.
//
// The wire protocol has no session token and none is missing: a connection holds
// its session for as long as it is open, so the credential in the first request
// is verified once and never again. That is what the HTTP surface has to buy back
// with a token.
type Credentials struct {
	User     string
	Password string
}

// A Refusal is the node declining a statement it understood — a syntax error, a
// permission, a constraint. The message is the store's own words, carried whole,
// and it does not close the connection.
type Refusal struct{ Message string }

func (r *Refusal) Error() string { return "tessaridb: refused: " + r.Message }

// A Redirect is an instruction, not a failure.
//
// This node cannot answer within the staleness bound it was given and another
// one can. It must never go through the error path of a client, and it must never
// be retried against the node that sent it.
type Redirect struct {
	Node     [16]byte
	Epoch    uint64
	Settled  bool
	Endpoint string
	Message  string
}

// Reply is what a statement produced: outcomes, or an instruction to ask
// elsewhere.
type Reply struct {
	Outcomes []Outcome
	Redirect *Redirect
}

// Conn is one connection to a node. It is not safe for concurrent use: the
// protocol is one request and one answer at a time on a socket.
type Conn struct {
	conn        net.Conn
	r           *bufio.Reader
	credentials *Credentials
	spent       bool
	subscribed  bool
	mu          sync.Mutex
}

// Dial opens a connection and exchanges the greeting.
//
// The address is a bare host:port. There is no URL scheme, and there is no TLS
// on this protocol — credentials travel as given, so run it on a protected
// network or behind something that terminates TLS.
func Dial(address string, credentials *Credentials) (*Conn, error) {
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("tessaridb: dialling %s: %w", address, err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		// Statements are small and synchronous; waiting to coalesce them adds
		// latency to every one and saves nothing.
		_ = tcp.SetNoDelay(true)
	}

	c := &Conn{conn: conn, r: bufio.NewReader(conn), credentials: credentials}
	if _, err := conn.Write(greeting()); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("tessaridb: sending the greeting: %w", err)
	}
	if err := readGreeting(c.r); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *Conn) Close() error { return c.conn.Close() }

var statementFrames = map[byte]bool{
	frameAnswer:    true,
	frameRefusal:   true,
	frameElsewhere: true,
}

// A declined Subscribe is answered with a REFUSAL, not with a change. This was
// found by a live node rather than by reading: without the refusal in this set a
// legitimate decline surfaced as "unknown frame kind", which tells the caller to
// upgrade the client — exactly the flattening the protocol's error taxonomy
// exists to prevent.
var changeFrames = map[byte]bool{
	frameChange:  true,
	frameRefusal: true,
}

// Execute runs a script and returns its outcomes.
//
// A refusal comes back as a *Refusal error; a redirect comes back in the Reply,
// because it is an instruction rather than a failure.
func (c *Conn) Execute(script string, parameters map[string]Value) (*Reply, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.subscribed {
		return nil, errors.New("tessaridb: this connection is subscribed and no longer answers statements — open a second one")
	}

	body, err := c.request(script, parameters)
	if err != nil {
		return nil, err
	}
	if err := writeFrame(c.conn, frameRequest, body); err != nil {
		return nil, err
	}

	kind, answer, err := readFrame(c.r, statementFrames)
	if err != nil {
		return nil, err
	}
	switch kind {
	case frameAnswer:
		outcomes, err := readAnswer(answer)
		return &Reply{Outcomes: outcomes}, err
	case frameRefusal:
		return nil, &Refusal{Message: string(answer)}
	case frameElsewhere:
		redirect, err := readElsewhere(answer)
		return &Reply{Redirect: redirect}, err
	}
	return nil, protocolf("a frame kind that reached the statement path: %d", kind)
}

func (c *Conn) request(script string, parameters map[string]Value) ([]byte, error) {
	w := &writer{}
	w.text(script)
	// The credential is spent on the first statement and never again — this is a
	// connection-scoped session, which is why there is no token here.
	if c.credentials != nil && !c.spent {
		w.u8(1)
		w.text(c.credentials.User)
		w.text(c.credentials.Password)
		c.spent = true
	} else {
		w.u8(0)
	}

	w.u32(uint32(len(parameters)))
	names := make([]string, 0, len(parameters))
	for name := range parameters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w.text(name)
		encoded, err := Encode(parameters[name])
		if err != nil {
			return nil, err
		}
		w.lenbytes(encoded)
	}
	return w.buf, nil
}

// A Change is one record written or removed, delivered on a subscription.
type Change struct {
	Sequence uint64
	Table    string
	Identity string
	Removed  bool
	Value    Value
}

// Changes subscribes this connection to the change stream.
//
// A subscription CONSUMES the connection: it answers no more statements
// afterwards. resumeAfter is the last sequence already seen and the +1 is applied
// here, because the protocol's own field is inclusive and owning the arithmetic
// in one place is the only way a client does not replay or skip one.
//
// The node drops a subscriber that stops reading after thirty seconds, so the
// returned channel must be drained.
func (c *Conn) Changes(resumeAfter uint64, fromStart bool, table string) (<-chan Change, <-chan error, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subscribed {
		return nil, nil, errors.New("tessaridb: this connection is already subscribed")
	}

	from := resumeAfter + 1
	if fromStart {
		from = 0
	}

	w := &writer{}
	w.u64(from)
	if table == "" {
		w.u8(0)
	} else {
		w.u8(1)
		w.text(table)
	}
	if err := writeFrame(c.conn, frameSubscribe, w.buf); err != nil {
		return nil, nil, err
	}
	c.subscribed = true

	changes := make(chan Change)
	fail := make(chan error, 1)
	go func() {
		defer close(changes)
		defer close(fail)
		for {
			kind, body, err := readFrame(c.r, changeFrames)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					fail <- err
				}
				return
			}
			if kind == frameRefusal {
				fail <- &Refusal{Message: string(body)}
				return
			}
			change, err := readChange(body)
			if err != nil {
				fail <- err
				return
			}
			changes <- change
		}
	}()
	return changes, fail, nil
}

func readChange(body []byte) (Change, error) {
	r := &reader{buf: body}
	sequence, err := r.u64("a change sequence")
	if err != nil {
		return Change{}, err
	}
	// The table arrives NAMED rather than identified: a subscriber has no names
	// block to resolve an id against.
	table, err := r.text("a change table")
	if err != nil {
		return Change{}, err
	}
	identity, err := r.text("a change identity")
	if err != nil {
		return Change{}, err
	}
	fate, err := r.u8("a change fate")
	if err != nil {
		return Change{}, err
	}
	switch fate {
	case 1:
		return Change{Sequence: sequence, Table: table, Identity: identity, Removed: true}, nil
	case 0:
		payload, err := r.lenbytes("a change value")
		if err != nil {
			return Change{}, err
		}
		value, err := Decode(payload)
		return Change{Sequence: sequence, Table: table, Identity: identity, Value: value}, err
	default:
		return Change{}, protocolf("a change fate byte of %d", fate)
	}
}

func readElsewhere(body []byte) (*Redirect, error) {
	r := &reader{buf: body}
	node, err := r.fixed(16, "a redirect's node")
	if err != nil {
		return nil, err
	}
	epoch, err := r.u64("a redirect's epoch")
	if err != nil {
		return nil, err
	}
	settlement, err := r.u8("a redirect's settlement")
	if err != nil {
		return nil, err
	}
	// Zero is deliberately unassigned, so it is refused rather than guessed at.
	if settlement != 1 && settlement != 2 {
		return nil, protocolf("a redirect settlement byte of %d", settlement)
	}
	endpoint, err := r.text("a redirect's endpoint")
	if err != nil {
		return nil, err
	}
	out := &Redirect{Epoch: epoch, Settled: settlement == 1, Endpoint: endpoint}
	copy(out.Node[:], node)
	return out, nil
}
