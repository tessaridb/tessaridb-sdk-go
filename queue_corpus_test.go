package tessaridb

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// The shared queue corpus, run against the type the worker builds every
// statement through: byte-identical text, identical parameter names, and every
// bad name or count refused before anything is sent.

type queueNode struct {
	Compare *struct {
		Field string            `json:"field"`
		Op    string            `json:"op"`
		Value map[string]string `json:"value"`
	} `json:"compare"`
	And []queueNode `json:"and"`
	Or  []queueNode `json:"or"`
}

type queueFields struct {
	Namespace string     `json:"namespace"`
	Database  string     `json:"database"`
	Queue     string     `json:"queue"`
	Worker    *string    `json:"worker"`
	Count     int        `json:"count"`
	Where     *queueNode `json:"where"`
	ID        string     `json:"id"`
	AfterMs   int64      `json:"after_ms"`
}

type queueCase struct {
	Name       string                               `json:"name"`
	Build      map[string]queueFields               `json:"build"`
	Script     string                               `json:"script"`
	Parameters map[string]map[string]string         `json:"parameters"`
	Refused    *struct{ Reason, What, Name string } `json:"refused"`
}

var queueOperators = map[string]Operator{"eq": Eq, "ne": Ne, "lt": Lt, "le": Le, "gt": Gt, "ge": Ge}

func queueValue(t *testing.T, encoded map[string]string) Value {
	t.Helper()
	if held, ok := encoded["string"]; ok {
		return Text{Value: held}
	}
	n, err := strconv.ParseInt(encoded["integer"], 10, 64)
	if err != nil {
		t.Fatalf("a value this test does not know: %v", encoded)
	}
	return Integer{Value: n}
}

func queueFilter(t *testing.T, node queueNode) Filter {
	t.Helper()
	switch {
	case node.Compare != nil:
		return Compare(node.Compare.Field, queueOperators[node.Compare.Op], queueValue(t, node.Compare.Value))
	case node.And != nil:
		return And(queueFilter(t, node.And[0]), queueFilter(t, node.And[1]))
	default:
		return Or(queueFilter(t, node.Or[0]), queueFilter(t, node.Or[1]))
	}
}

func renderQueueCase(t *testing.T, kind string, fields queueFields) (string, map[string]map[string]string, error) {
	t.Helper()
	worker := "worker"
	if fields.Worker != nil {
		worker = *fields.Worker
	}
	statements, err := newQueueStatements(fields.Namespace, fields.Database, fields.Queue, worker)
	if err != nil {
		return "", nil, err
	}
	var (
		script     string
		parameters map[string]Value
	)
	switch kind {
	case "claim":
		var where Filter
		if fields.Where != nil {
			where = queueFilter(t, *fields.Where)
		}
		script, parameters, err = statements.claim(fields.Count, where)
		if err != nil {
			return "", nil, err
		}
	case "done":
		script = statements.done(fields.ID)
	case "release":
		script = statements.release(fields.ID, time.Duration(fields.AfterMs)*time.Millisecond)
	default:
		t.Fatalf("a build kind this test does not know: %s", kind)
	}
	bound := make(map[string]map[string]string, len(parameters))
	for name, value := range parameters {
		switch held := value.(type) {
		case Text:
			bound[name] = map[string]string{"string": held.Value}
		case Integer:
			bound[name] = map[string]string{"integer": strconv.FormatInt(held.Value, 10)}
		default:
			t.Fatalf("%s is bound as %T", name, value)
		}
	}
	return script, bound, nil
}

func TestEveryQueueStatementRendersAsTheCorpusSays(t *testing.T) {
	var cases []queueCase
	if err := json.Unmarshal(readCorpus(t, "queue-v1.json")["cases"], &cases); err != nil {
		t.Fatalf("the corpus cases: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("a corpus with no cases checks nothing")
	}
	for _, each := range cases {
		t.Run(each.Name, func(t *testing.T) {
			for kind, fields := range each.Build {
				script, parameters, err := renderQueueCase(t, kind, fields)
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
