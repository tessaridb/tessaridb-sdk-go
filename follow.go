package tessaridb

import (
	"fmt"
	"regexp"
)

// Following a redirect (§3.12) to the node that should answer.
//
//   - At most three hops: a fourth redirect is a loop, or a cluster moving faster
//     than a request can follow it, and going on would not tell them apart.
//   - Epochs never go backwards: a redirect dated by an older leadership than one
//     already followed was decided before it, and points at the past.
//   - The node there is the node named: session::context() on arrival says which
//     node took the connection.
//   - The tenancy goes with the request, each name checked as a plain name and
//     never quoted into a script.

const (
	mostHops      = 3
	contextScript = "RETURN session::context();"
)

var plainName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// RedirectLoopError: three redirects followed and still no answer.
type RedirectLoopError struct{ Hops int }

func (e *RedirectLoopError) Error() string {
	return fmt.Sprintf("tessaridb: still redirected after %d hops; stopping rather than going round", e.Hops)
}

// StaleRedirectError: a redirect dated by an older leadership than one this
// request already followed.
type StaleRedirectError struct{ Epoch, Floor uint64 }

func (e *StaleRedirectError) Error() string {
	return fmt.Sprintf("tessaridb: redirected under epoch %d after following epoch %d", e.Epoch, e.Floor)
}

// WrongNodeError: the address a redirect named answered as another node, and the
// request was not sent there.
type WrongNodeError struct{ Expected [16]byte }

func (e *WrongNodeError) Error() string {
	return "tessaridb: the redirect named another node than the one that answered there"
}

// NotFollowableError: the session's namespace or database is not a plain name,
// so it is not selected again on the node a redirect named — a name is grammar,
// and this client does not quote one into a script.
type NotFollowableError struct{ Name string }

func (e *NotFollowableError) Error() string {
	return fmt.Sprintf("tessaridb: cannot follow: %q is not a plain name to select on the other node", e.Name)
}

// follow sends the request where first says, and on, until something answers.
// The caller holds c.mu.
func (c *Conn) follow(script string, parameters map[string]Value, first *Redirect) (*Reply, error) {
	here, err := c.context()
	if err != nil {
		return nil, err
	}
	selecting, err := selection(here)
	if err != nil {
		return nil, err
	}
	redirect, floor := first, uint64(0)
	for hops := 0; ; hops++ {
		if hops >= mostHops {
			return nil, &RedirectLoopError{Hops: hops}
		}
		if redirect.Epoch < floor {
			return nil, &StaleRedirectError{Epoch: redirect.Epoch, Floor: floor}
		}
		floor = redirect.Epoch
		there, err := dial(redirect.Endpoint, c.credentials, c.trust)
		if err != nil {
			return nil, err
		}
		reply, err := there.arrive(redirect.Node, selecting, script, parameters)
		if err != nil {
			_ = there.Close()
			return nil, err
		}
		if reply.Redirect == nil {
			if redirect.Settled {
				_ = c.conn.Close()
				c.conn, c.r, c.peerMinor, c.spent = there.conn, there.r, there.peerMinor, there.spent
			} else {
				_ = there.Close()
			}
			return reply, nil
		}
		_ = there.Close()
		redirect = reply.Redirect
	}
}

// arrive checks that this is node, selects the tenancy and sends the request.
func (c *Conn) arrive(node [16]byte, selecting, script string, parameters map[string]Value) (*Reply, error) {
	context, err := c.context()
	if err != nil {
		return nil, err
	}
	if id, ok := context["node"].(UUID); !ok || id.Value != node {
		return nil, &WrongNodeError{Expected: node}
	}
	if selecting != "" {
		if _, err := c.ask(selecting, nil); err != nil {
			return nil, err
		}
	}
	return c.ask(script, parameters)
}

// context is what session::context() answers on this connection.
func (c *Conn) context() (map[string]Value, error) {
	reply, err := c.ask(contextScript, nil)
	if err != nil {
		return nil, err
	}
	if len(reply.Outcomes) == 0 {
		return nil, protocolf("session::context() answered nothing")
	}
	answered, ok := reply.Outcomes[len(reply.Outcomes)-1].(ValueOutcome)
	if !ok {
		return nil, protocolf("session::context() answers one value")
	}
	object, ok := answered.Value.(Object)
	if !ok {
		return nil, protocolf("session::context() answers one object")
	}
	return object.Fields, nil
}

// selection is the USE that selects context's tenancy again, or "".
func selection(context map[string]Value) (string, error) {
	script := ""
	for _, pair := range [][2]string{{"NAMESPACE", "namespace"}, {"DATABASE", "database"}} {
		name, ok := context[pair[1]].(Text)
		if !ok {
			continue
		}
		if !plainName.MatchString(name.Value) {
			return "", &NotFollowableError{Name: name.Value}
		}
		script += "USE " + pair[0] + " " + name.Value + "; "
	}
	return script, nil
}
