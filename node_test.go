package tessaridb

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Exercised against a running node.
//
// Opt-in, because a suite that needs a server cannot be the suite that runs on a
// clean checkout. These are also the only tests here that prove SEMANTICS:
// everything else proves this client agrees with the specification's bytes, and a
// client can agree with the bytes and still ask the wrong question.
//
//	TESSARIDB_TEST_NODE=127.0.0.1:47915 go test ./...
//
// The suite seeds its own fixture and owns every record it asserts on.

func node(t *testing.T) *Conn {
	t.Helper()
	address := os.Getenv("TESSARIDB_TEST_NODE")
	if address == "" {
		t.Skip("set TESSARIDB_TEST_NODE=<host:port> to run the live tests")
	}
	conn, err := Dial(address, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

const schema = `
DEFINE NAMESPACE IF NOT EXISTS gocorpus;
USE NAMESPACE gocorpus;
DEFINE DATABASE IF NOT EXISTS app;
USE DATABASE app;
DEFINE COLLECTION IF NOT EXISTS thing;
`

const use = "USE NAMESPACE gocorpus; USE DATABASE app;"

func seed(t *testing.T, c *Conn) {
	t.Helper()
	run(t, c, schema)
	// Emptied first: these tests select by predicate as well as by identity, so
	// the collection has to hold what this function put there and nothing else.
	// A delete over a set must state its ceiling.
	run(t, c, use+" DELETE FROM thing WHERE true LIMIT ALL;")
	run(t, c, use+` CREATE thing:1 = { name: 'alice', n: 42,
		at: datetime '2026-09-17T00:00:00Z',
		spot: geometry { type: 'Point', coordinates: [2.3522, 48.8566] } };`)
}

func run(t *testing.T, c *Conn, script string) *Reply {
	t.Helper()
	reply, err := c.Execute(script, nil)
	if err != nil {
		t.Fatalf("execute %.60q: %v", script, err)
	}
	if reply.Redirect != nil {
		t.Fatalf("the node redirected to %s", reply.Redirect.Endpoint)
	}
	return reply
}

func TestAGreetingIsExchangedAndASelectKeepsItsTypes(t *testing.T) {
	c := node(t)
	seed(t, c)

	reply := run(t, c, use+" SELECT * FROM thing:1;")
	records, ok := reply.Outcomes[len(reply.Outcomes)-1].(Records)
	if !ok {
		t.Fatalf("expected records, got %T", reply.Outcomes[len(reply.Outcomes)-1])
	}
	if len(records.Rows) != 1 {
		t.Fatalf("expected exactly the record we wrote, got %d", len(records.Rows))
	}

	object, ok := records.Rows[0].Value.(Object)
	if !ok {
		t.Fatalf("a record is an object, got %T", records.Rows[0].Value)
	}
	// The whole point of the wire protocol: these come back as themselves rather
	// than narrowed into JSON's six types.
	if n, ok := object.Fields["n"].(Integer); !ok || n.Value != 42 {
		t.Fatalf("n came back as %#v", object.Fields["n"])
	}
	if _, ok := object.Fields["at"].(Datetime); !ok {
		t.Fatalf("at came back as %T, not a datetime", object.Fields["at"])
	}
	spot, ok := object.Fields["spot"].(Geometry)
	if !ok {
		t.Fatalf("spot came back as %T, not a geometry", object.Fields["spot"])
	}
	point, ok := spot.Shape.(Point)
	if !ok {
		t.Fatalf("spot is a %T", spot.Shape)
	}
	// Longitude first, and Paris is at 2.35E 48.86N rather than the other way
	// round, which is a perfectly valid place in the Indian Ocean.
	if point.Position.Lon < 2.3 || point.Position.Lon > 2.4 {
		t.Fatalf("longitude came back as %v — the coordinates are the wrong way round", point.Position.Lon)
	}
}

func TestAParameterIsBoundAsAValueNeverFormattedIntoTheScript(t *testing.T) {
	c := node(t)
	seed(t, c)

	hostile := "'; DROP COLLECTION thing; --"
	reply, err := c.Execute(use+" RETURN $x;", map[string]Value{"x": Text{Value: hostile}})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	value, ok := reply.Outcomes[len(reply.Outcomes)-1].(ValueOutcome)
	if !ok {
		t.Fatalf("expected a value, got %T", reply.Outcomes[len(reply.Outcomes)-1])
	}
	text, ok := value.Value.(Text)
	if !ok || text.Value != hostile {
		t.Fatalf("the parameter came back as %#v", value.Value)
	}

	// And the collection it named is still there.
	run(t, c, use+" SELECT * FROM thing:1;")
}

func TestARefusalCarriesTheStoresOwnWordsAndDoesNotCloseTheConnection(t *testing.T) {
	c := node(t)
	seed(t, c)

	_, err := c.Execute(use+" SELECT * FROM nosuchtable;", nil)
	refusal, ok := err.(*Refusal)
	if !ok {
		t.Fatalf("expected a refusal, got %T: %v", err, err)
	}
	if !strings.Contains(refusal.Message, "nosuchtable") {
		t.Fatalf("the refusal should carry the store's own words: %q", refusal.Message)
	}
	// The connection survives it — a refusal is an answer, not a fault.
	run(t, c, use+" SELECT * FROM thing:1;")
}

func TestAConnectionHoldsOneSessionAcrossStatements(t *testing.T) {
	c := node(t)
	run(t, c, schema)
	// The USE in one statement is still in force in the next, which is what makes
	// this a session rather than a sequence of independent requests.
	run(t, c, use)
	run(t, c, "SELECT * FROM thing LIMIT 1;")
}

func TestASubscriptionDeliversAChangeWrittenByAnotherConnection(t *testing.T) {
	address := os.Getenv("TESSARIDB_TEST_NODE")
	if address == "" {
		t.Skip("set TESSARIDB_TEST_NODE=<host:port> to run the live tests")
	}

	setup := node(t)
	seed(t, setup)

	watcher, err := Dial(address, nil)
	if err != nil {
		t.Fatalf("dial the watcher: %v", err)
	}
	defer watcher.Close()
	run(t, watcher, use)

	changes, fail, err := watcher.Changes(0, true, "")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// A unique identity per run, because a subscription from the start replays
	// history and the first change delivered is somebody else's.
	identity := fmt.Sprintf("watched-%d", time.Now().UnixNano())
	run(t, setup, use+fmt.Sprintf(" CREATE thing:'%s' = { seen: true };", identity))

	deadline := time.After(10 * time.Second)
	for {
		select {
		case change, open := <-changes:
			if !open {
				t.Fatal("the change stream closed before the record arrived")
			}
			if change.Identity == identity {
				if change.Removed {
					t.Fatal("the record was written, not removed")
				}
				return
			}
		case err := <-fail:
			t.Fatalf("the subscription failed: %v", err)
		case <-deadline:
			t.Fatal("the change never arrived")
		}
	}
}

func TestAValueOutcomeCarriesALengthBeforeItsValue(t *testing.T) {
	// The bug this test exists for: §3.5 writes the Value outcome as
	// "names · `bytes` value", and `bytes` at the frame layer is a u32 length
	// then the bytes. A client that reads the value raw reads the length's first
	// byte as a type tag — 0x00, which is not one — and fails loudly.
	//
	// Loudly, but only for a client that ever asks for a value outcome. A suite
	// that only SELECTs never produces one, which is how it shipped in the
	// TypeScript client and was found here instead.
	c := node(t)
	seed(t, c)

	for _, probe := range []struct {
		script string
		check  func(Value) bool
	}{
		{"RETURN 1;", func(v Value) bool { n, ok := v.(Integer); return ok && n.Value == 1 }},
		{"RETURN 'hello';", func(v Value) bool { s, ok := v.(Text); return ok && s.Value == "hello" }},
		{"RETURN [1, 2];", func(v Value) bool { a, ok := v.(Array); return ok && len(a.Items) == 2 }},
		{"RETURN NONE;", func(v Value) bool { _, ok := v.(None); return ok }},
	} {
		reply := run(t, c, use+" "+probe.script)
		outcome, ok := reply.Outcomes[len(reply.Outcomes)-1].(ValueOutcome)
		if !ok {
			t.Fatalf("%s: expected a value outcome, got %T", probe.script, reply.Outcomes[len(reply.Outcomes)-1])
		}
		if !probe.check(outcome.Value) {
			t.Fatalf("%s: came back as %#v", probe.script, outcome.Value)
		}
	}
}
