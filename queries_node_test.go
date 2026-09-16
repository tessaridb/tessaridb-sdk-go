package tessaridb

import "testing"

// Every rendered corpus case, executed by a node's own parser.
//
// The corpus proves two implementations of the rendering contract agree. It
// cannot prove either of them agrees with the PARSER, because no client may link
// it — so a rendering both clients got wrong in the same way would pass the
// corpus and fail against every real node. Running the text is the only check
// that reaches the thing that has to accept it.
//
//	TESSARIDB_TEST_NODE=127.0.0.1:47915 go test ./...

const queryFixture = `
DEFINE NAMESPACE IF NOT EXISTS gocorpus;
USE NAMESPACE gocorpus;
DEFINE DATABASE IF NOT EXISTS app;
USE DATABASE app;
DEFINE COLLECTION IF NOT EXISTS memories;
`

func TestEveryRenderedCorpusCaseIsRunByANode(t *testing.T) {
	conn := node(t)
	run(t, conn, queryFixture)
	// The rendered statements carry no USE clause — they name one collection and
	// nothing else — so the session supplies the namespace. It survives across
	// statements on this connection, which is what makes that possible.
	run(t, conn, use+" DELETE FROM memories WHERE true LIMIT ALL;")

	executed := 0
	for _, c := range queryCases(t) {
		if c.Refused != nil {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			result, err := statementOf(t, c.Build).Render()
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			reply, err := conn.Execute(result.Script, result.Parameters)
			if err != nil {
				t.Fatalf("the node refused %q: %v", result.Script, err)
			}
			if reply.Redirect != nil {
				t.Fatalf("the node redirected to %s", reply.Redirect.Endpoint)
			}
			if len(reply.Outcomes) == 0 {
				t.Fatalf("the node answered %q with no outcome at all", result.Script)
			}
			executed++
		})
	}
	t.Logf("executed by the node: %d of the corpus's rendered cases", executed)
}
