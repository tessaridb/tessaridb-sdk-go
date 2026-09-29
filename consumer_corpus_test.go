package tessaridb

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// The shared consumer corpus, run against the type the consumer builds every
// statement through. Each client's live consumer test proves only that its own
// node accepts what it sends; this corpus, rendered by a second implementation
// of spec/consumer-v1.md, is what makes the five clients agree with one another.

type consumerFields struct {
	Namespace string   `json:"namespace"`
	Database  string   `json:"database"`
	Topic     string   `json:"topic"`
	Group     string   `json:"group"`
	Limit     int      `json:"limit"`
	Positions []uint64 `json:"positions"`
	DelayMs   int64    `json:"delay_ms"`
}

type consumerCase struct {
	Name       string                               `json:"name"`
	Build      map[string]consumerFields            `json:"build"`
	Script     string                               `json:"script"`
	Parameters map[string]map[string]string         `json:"parameters"`
	Refused    *struct{ Reason, What, Name string } `json:"refused"`
}

func renderConsumerCase(t *testing.T, kind string, fields consumerFields, statements consumerStatements) (string, map[string]map[string]string) {
	t.Helper()
	var (
		script     string
		parameters map[string]Value
		err        error
	)
	switch kind {
	case "read":
		script = statements.read(fields.Limit)
	case "ack":
		script, parameters, err = statements.ack(fields.Positions)
	case "nack":
		script, parameters, err = statements.nack(time.Duration(fields.DelayMs)*time.Millisecond, fields.Positions)
	default:
		t.Fatalf("a build kind this test does not know: %s", kind)
	}
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	bound := make(map[string]map[string]string, len(parameters))
	for name, value := range parameters {
		held, ok := value.(Integer)
		if !ok {
			t.Fatalf("%s is bound as %T, not an integer", name, value)
		}
		bound[name] = map[string]string{"integer": strconv.FormatInt(held.Value, 10)}
	}
	return script, bound
}

func TestEveryConsumerStatementRendersAsTheCorpusSays(t *testing.T) {
	var cases []consumerCase
	if err := json.Unmarshal(readCorpus(t, "consumer-v1.json")["cases"], &cases); err != nil {
		t.Fatalf("the corpus cases: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("a corpus with no cases checks nothing")
	}
	for _, each := range cases {
		t.Run(each.Name, func(t *testing.T) {
			for kind, fields := range each.Build {
				statements, err := newConsumerStatements(fields.Namespace, fields.Database, fields.Topic, fields.Group)
				if each.Refused != nil {
					var refusal *BuilderError
					if !errors.As(err, &refusal) {
						t.Fatalf("rendered a case the corpus refuses: %v", err)
					}
					got := struct{ Reason, What, Name string }{string(refusal.Reason), refusal.What, refusal.Name}
					if got != *each.Refused {
						t.Fatalf("refused as %+v, the corpus says %+v", got, *each.Refused)
					}
					return
				}
				if err != nil {
					t.Fatalf("refused a case the corpus renders: %v", err)
				}
				script, parameters := renderConsumerCase(t, kind, fields, statements)
				if script != each.Script {
					t.Fatalf("script\n got %q\nwant %q", script, each.Script)
				}
				if !reflect.DeepEqual(parameters, each.Parameters) {
					t.Fatalf("parameters\n got %v\nwant %v", parameters, each.Parameters)
				}
			}
		})
	}
}
