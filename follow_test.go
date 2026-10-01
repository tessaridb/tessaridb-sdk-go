package tessaridb

// Following a redirect (§3.12), against scripted nodes on loopback. Each fake
// greets, keeps its own session's USE, answers session::context() as a node
// does, and hands every other script to the test's function.

import (
	"bufio"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
)

var (
	nodeA = [16]byte{0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa, 0xa}
	nodeB = [16]byte{0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb, 0xb}
	nodeC = [16]byte{0xc, 0xc, 0xc, 0xc, 0xc, 0xc, 0xc, 0xc, 0xc, 0xc, 0xc, 0xc, 0xc, 0xc, 0xc, 0xc}
)

const followRead = "SELECT * FROM ledger;"

// fakeReply is a value, or a redirect when go is set.
type fakeReply struct {
	value Value
	go_   *Redirect
}

type fake struct {
	node     [16]byte
	claims   [16]byte
	behave   func(script string) fakeReply
	address  string
	mu       sync.Mutex
	seen     []string
	signed   []string
	listener net.Listener
}

func startFake(t *testing.T, node [16]byte, behave func(string) fakeReply) *fake {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{node: node, claims: node, behave: behave, address: listener.Addr().String(), listener: listener}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fake) log() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...), append([]string(nil), f.signed...)
}

func (f *fake) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	var hello [6]byte
	if _, err := io.ReadFull(r, hello[:]); err != nil {
		return
	}
	_, _ = conn.Write([]byte{'T', 'E', 'S', 'S', 1, 2})
	var namespace, database Value = Null{}, Null{}
	for {
		_, body, err := readFrame(r, map[byte]bool{frameRequest: true})
		if err != nil {
			return
		}
		rd := &reader{buf: body}
		script, _ := rd.text("script")
		if signed, _ := rd.u8("signed"); signed == 1 {
			user, _ := rd.text("user")
			f.mu.Lock()
			f.signed = append(f.signed, user)
			f.mu.Unlock()
		}
		f.mu.Lock()
		f.seen = append(f.seen, script)
		f.mu.Unlock()
		var reply fakeReply
		switch {
		case script == contextScript:
			reply.value = Object{Fields: map[string]Value{
				"node": UUID{Value: f.claims}, "namespace": namespace, "database": database,
			}}
		case strings.HasPrefix(script, "USE "):
			for _, statement := range strings.Split(script, ";") {
				words := strings.Fields(statement)
				if len(words) == 3 && words[1] == "NAMESPACE" {
					namespace = Text{Value: words[2]}
				}
				if len(words) == 3 && words[1] == "DATABASE" {
					database = Text{Value: words[2]}
				}
			}
			reply.value = Null{}
		default:
			reply = f.behave(script)
		}
		if reply.go_ != nil {
			w := &writer{}
			w.fixed(reply.go_.Node[:])
			w.u64(reply.go_.Epoch)
			if reply.go_.Settled {
				w.u8(1)
			} else {
				w.u8(2)
			}
			w.text(reply.go_.Endpoint)
			_ = writeFrame(conn, frameElsewhere, w.buf)
			continue
		}
		encoded, _ := Encode(reply.value)
		outcome := &writer{}
		outcome.u8(2)
		outcome.u32(0)
		outcome.lenbytes(encoded)
		w := &writer{}
		w.u32(1)
		w.lenbytes(outcome.buf)
		_ = writeFrame(conn, frameAnswer, w.buf)
	}
}

func answering(n int64) func(string) fakeReply {
	return func(string) fakeReply { return fakeReply{value: Integer{Value: n}} }
}

// sending sends followRead to `to`; anything else is answered here.
func sending(to *fake, epoch uint64, settled bool) func(string) fakeReply {
	return func(script string) fakeReply {
		if script != followRead {
			return fakeReply{value: Integer{Value: 1}}
		}
		return fakeReply{go_: &Redirect{Node: to.node, Epoch: epoch, Settled: settled, Endpoint: to.address}}
	}
}

func selectedOn(t *testing.T, origin *fake) *Conn {
	t.Helper()
	conn, err := Dial(origin.address, &Credentials{User: "ada", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Execute("USE NAMESPACE prod; USE DATABASE shop;", nil); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestATransientRedirectAnswersThereAndLeavesTheConnectionHere(t *testing.T) {
	b := startFake(t, nodeB, answering(42))
	a := startFake(t, nodeA, sending(b, 7, false))
	conn := selectedOn(t, a)
	reply, err := conn.Execute(followRead, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := reply.Outcomes[len(reply.Outcomes)-1].(ValueOutcome).Value; !reflect.DeepEqual(got, Integer{Value: 42}) {
		t.Fatalf("answered %v", got)
	}
	seen, signed := b.log()
	want := []string{contextScript, "USE NAMESPACE prod; USE DATABASE shop; ", followRead}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("B saw %q", seen)
	}
	if len(signed) == 0 || signed[0] != "ada" {
		t.Fatalf("the credentials were not presented there: %q", signed)
	}
	if _, err := conn.Execute("RETURN 1;", nil); err != nil {
		t.Fatal(err)
	}
	if seenA, _ := a.log(); seenA[len(seenA)-1] != "RETURN 1;" {
		t.Fatalf("A last saw %q", seenA[len(seenA)-1])
	}
	if seen, _ := b.log(); len(seen) != 3 {
		t.Fatalf("B was asked again: %q", seen)
	}
}

func TestASettledRedirectMovesTheConnectionThere(t *testing.T) {
	b := startFake(t, nodeB, answering(42))
	a := startFake(t, nodeA, sending(b, 7, true))
	conn := selectedOn(t, a)
	if _, err := conn.Execute(followRead, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Execute("RETURN 1;", nil); err != nil {
		t.Fatal(err)
	}
	if seen, _ := b.log(); seen[len(seen)-1] != "RETURN 1;" {
		t.Fatalf("B last saw %q", seen[len(seen)-1])
	}
	if seen, _ := a.log(); strings.Contains(strings.Join(seen, "|"), "RETURN 1;") {
		t.Fatal("A was not left")
	}
}

func TestANodeOtherThanTheOneNamedIsNotSentTheRequest(t *testing.T) {
	b := startFake(t, nodeB, answering(42))
	b.claims = nodeC
	a := startFake(t, nodeA, sending(b, 7, false))
	conn := selectedOn(t, a)
	_, err := conn.Execute(followRead, nil)
	var wrong *WrongNodeError
	if !errors.As(err, &wrong) || wrong.Expected != nodeB {
		t.Fatalf("expected WrongNodeError, got %v", err)
	}
	if seen, _ := b.log(); strings.Contains(strings.Join(seen, "|"), followRead) {
		t.Fatal("B was sent the request")
	}
}

func TestARedirectDatedBeforeOneAlreadyFollowedIsRefused(t *testing.T) {
	c := startFake(t, nodeC, answering(42))
	b := startFake(t, nodeB, sending(c, 3, false))
	a := startFake(t, nodeA, sending(b, 5, false))
	conn := selectedOn(t, a)
	_, err := conn.Execute(followRead, nil)
	var stale *StaleRedirectError
	if !errors.As(err, &stale) || stale.Epoch != 3 || stale.Floor != 5 {
		t.Fatalf("expected StaleRedirectError 3 after 5, got %v", err)
	}
	if seen, _ := c.log(); len(seen) != 0 {
		t.Fatalf("C was dialled: %q", seen)
	}
}

func TestThreeHopsAndNoAnswerIsALoop(t *testing.T) {
	var c *fake
	c = startFake(t, nodeC, func(string) fakeReply {
		return fakeReply{go_: &Redirect{Node: nodeC, Epoch: 1, Endpoint: c.address}}
	})
	a := startFake(t, nodeA, sending(c, 1, false))
	conn := selectedOn(t, a)
	_, err := conn.Execute(followRead, nil)
	var loop *RedirectLoopError
	if !errors.As(err, &loop) || loop.Hops != 3 {
		t.Fatalf("expected RedirectLoopError after 3, got %v", err)
	}
	seen, _ := c.log()
	if n := strings.Count(strings.Join(seen, "|"), followRead); n != 3 {
		t.Fatalf("sent %d times, want 3", n)
	}
}

func TestATenancyThatIsNotAPlainNameIsNotFollowed(t *testing.T) {
	b := startFake(t, nodeB, answering(42))
	a := startFake(t, nodeA, sending(b, 7, false))
	conn, err := Dial(a.address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Execute("USE NAMESPACE pr-od;", nil); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Execute(followRead, nil)
	var refused *NotFollowableError
	if !errors.As(err, &refused) || refused.Name != "pr-od" {
		t.Fatalf("expected NotFollowableError, got %v", err)
	}
	if seen, _ := b.log(); len(seen) != 0 {
		t.Fatalf("B was dialled: %q", seen)
	}
}
