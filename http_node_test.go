package tessaridb

import (
	"os"
	"strings"
	"testing"
)

// The HTTP surface against a running node.
//
//	TESSARIDB_TEST_HTTP=127.0.0.1:47916 go test ./...
//
// and, for the session token, a store that has a user declared — which is the
// only thing that makes a token exist at all:
//
//	TESSARIDB_TEST_HTTP_CLOSED=127.0.0.1:47918 TESSARIDB_TEST_USER=corpus \
//	TESSARIDB_TEST_PASSWORD=… go test ./...

const httpFixture = `
DEFINE NAMESPACE IF NOT EXISTS gocorpus;
USE NAMESPACE gocorpus;
DEFINE DATABASE IF NOT EXISTS app;
USE DATABASE app;
DEFINE COLLECTION IF NOT EXISTS memories;
DEFINE BUCKET IF NOT EXISTS uploads;
`

func httpNode(t *testing.T) *HTTPClient {
	t.Helper()
	address := os.Getenv("TESSARIDB_TEST_HTTP")
	if address == "" {
		t.Skip("set TESSARIDB_TEST_HTTP=<host:port> to run the live HTTP tests")
	}
	return NewHTTPClient(address, nil)
}

func closedNode(t *testing.T) *HTTPClient {
	t.Helper()
	address := os.Getenv("TESSARIDB_TEST_HTTP_CLOSED")
	if address == "" {
		t.Skip("set TESSARIDB_TEST_HTTP_CLOSED=<host:port> to run the session tests")
	}
	return NewHTTPClient(address, &Credentials{
		User:     os.Getenv("TESSARIDB_TEST_USER"),
		Password: os.Getenv("TESSARIDB_TEST_PASSWORD"),
	})
}

func TestTheNodeReportsItsConditionOnBothRoutes(t *testing.T) {
	node := httpNode(t)
	for _, route := range []struct {
		name string
		ask  func() (Health, error)
	}{{"health", node.Health}, {"ready", node.Ready}} {
		condition, err := route.ask()
		if err != nil {
			t.Fatalf("%s: %v", route.name, err)
		}
		if _, well := condition.(Healthy); !well {
			t.Fatalf("%s: a running node is well, got %#v", route.name, condition)
		}
	}
}

func TestAScriptAnswersOneOutcomePerStatement(t *testing.T) {
	node := httpNode(t)
	if _, err := node.Script(httpFixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	results, err := node.Script(use + " RETURN 1; SELECT * FROM memories;")
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	// Two USE statements, then the two that answer.
	if len(results) != 4 {
		t.Fatalf("one outcome per statement: want 4, got %d", len(results))
	}
	value, err := ReadOutcome(results[2], Reading{Value: IntegerKind{}})
	if err != nil {
		t.Fatalf("the value outcome: %v", err)
	}
	if got, ok := value.(ScriptValue); !ok || !sameValue(Integer{Value: 1}, got.Value) {
		t.Fatalf("RETURN 1 answers the integer 1, got %#v", value)
	}
	records, err := ReadOutcome(results[3], Reading{Value: ObjectKind{}})
	if err != nil {
		t.Fatalf("the records outcome: %v", err)
	}
	if _, ok := records.(ScriptRecords); !ok {
		t.Fatalf("a SELECT answers records, got %#v", records)
	}
}

// §5.6's own example writes a row's id as "users:1" while the corpus writes the
// id half alone. A client cannot implement both, so this asks the node.
func TestARowIdentityIsTheIdHalfAlone(t *testing.T) {
	node := httpNode(t)
	if _, err := node.Script(httpFixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if _, err := node.Script(use + " DELETE FROM memories WHERE true LIMIT ALL;" +
		" CREATE memories:4242 = { body: 'a row with a known identity' };"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	results, err := node.Script(use + " SELECT * FROM memories;")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	outcome, err := ReadOutcome(results[len(results)-1], Reading{Value: ObjectKind{}})
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	records, ok := outcome.(ScriptRecords)
	if !ok || len(records.Rows) != 1 {
		t.Fatalf("one seeded record, got %#v", outcome)
	}
	if records.Rows[0].ID != "4242" {
		t.Fatalf("the node writes a row id as %q, not the id half alone — "+
			"§5.6's example and the corpus disagree and this is the answer",
			records.Rows[0].ID)
	}
}

// A failed multi-statement script reports no partial results at all.
func TestAFailedScriptReportsNoPartialResults(t *testing.T) {
	node := httpNode(t)
	_, err := node.Script(use + " RETURN 1; SELECT * FROM nosuchcollection;")
	if err == nil {
		t.Fatal("a script naming no such collection is refused")
	}
	refused, ok := err.(*HTTPError)
	if !ok || refused.Status != 400 {
		t.Fatalf("want a 400, got %v", err)
	}
}

func TestAFileSurvivesAPathThatNeedsEncoding(t *testing.T) {
	node := httpNode(t)
	if _, err := node.Script(httpFixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	// A space and a percent: the first makes the request line unparseable if it
	// is not encoded, the second asks the server to decode an escape nobody
	// wrote.
	const name = "reports/100% done.txt"
	content := []byte("four")

	if err := node.Put("gocorpus", "app", "uploads", name, content); err != nil {
		t.Fatalf("put: %v", err)
	}
	back, err := node.Get("gocorpus", "app", "uploads", name)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(back) != string(content) {
		t.Fatalf("the file came back as %q", back)
	}

	listing, err := node.List("gocorpus", "app", "uploads")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, entry := range listing {
		if strings.Contains(entry.Path, "100% done.txt") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the listing does not carry the file: %#v", listing)
	}

	if err := node.Remove("gocorpus", "app", "uploads", name); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// A 404 is an answer, and deletion is idempotent.
	gone, err := node.Get("gocorpus", "app", "uploads", name)
	if err != nil || gone != nil {
		t.Fatalf("a removed file reads as absent, got %q %v", gone, err)
	}
	if err := node.Remove("gocorpus", "app", "uploads", name); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
}

func TestANameThatIsNotABucketListsAsAbsent(t *testing.T) {
	node := httpNode(t)
	listing, err := node.List("gocorpus", "app", "memories")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if listing != nil {
		t.Fatalf("a collection is not a bucket, got %#v", listing)
	}
}

func TestBackupReturnsTheWholeLog(t *testing.T) {
	node := httpNode(t)
	log, err := node.Backup(nil)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if len(log) == 0 {
		t.Fatal("a store that has been written to has a log")
	}
}

// An open store has no session to open, and that is not a failure.
func TestAnOpenStoreMintsNoToken(t *testing.T) {
	node := httpNode(t)
	opened, err := node.OpenSession()
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if opened {
		t.Fatal("a store with no user declared has nothing to prove and nothing to save")
	}
}

func TestAClosedStoreSpendsThePasswordOnceAndPresentsATokenAfter(t *testing.T) {
	node := closedNode(t)
	opened, err := node.OpenSession()
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	if !opened {
		t.Fatal("a store with a user declared mints a token")
	}

	node.mu.Lock()
	token := node.token
	node.mu.Unlock()
	if len(token) != 64 {
		t.Fatalf("a token is always exactly 64 hexadecimal characters, got %d", len(token))
	}
	if strings.Trim(token, "0123456789abcdef") != "" {
		t.Fatalf("a token is lowercase hex, got %q", token)
	}

	// The same token answers the next call rather than a second password.
	if _, err := node.Script("RETURN 1;"); err != nil {
		t.Fatalf("a statement under the token: %v", err)
	}
	node.mu.Lock()
	after := node.token
	node.mu.Unlock()
	if after != token {
		t.Fatal("a working token is reused rather than re-minted per request")
	}

	if err := node.CloseSession(); err != nil {
		t.Fatalf("close session: %v", err)
	}
}

func TestAWrongPasswordSurfacesTheRefusalRatherThanLooping(t *testing.T) {
	address := os.Getenv("TESSARIDB_TEST_HTTP_CLOSED")
	if address == "" {
		t.Skip("set TESSARIDB_TEST_HTTP_CLOSED=<host:port> to run the session tests")
	}
	node := NewHTTPClient(address, &Credentials{User: "corpus", Password: "not the password"})
	opened, err := node.OpenSession()
	if err != nil {
		t.Fatalf("a refused password is a 401, not a transport error: %v", err)
	}
	if opened {
		t.Fatal("a wrong password does not open a session")
	}
	_, err = node.Script("RETURN 1;")
	refused, ok := err.(*HTTPError)
	if !ok || refused.Status != 401 {
		t.Fatalf("want a 401, got %v", err)
	}
}
