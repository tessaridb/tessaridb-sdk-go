package tessaridb

import (
	"encoding/json"
	"testing"
)

// The shared query corpus.
//
// The value corpus proves this client agrees with the specification's bytes; this
// one proves it agrees with the rendering contract's TEXT, character for
// character, including the parameter numbering. Two builders that render the same
// query differently produce two different statements, and the divergence only
// ever surfaces as a user reporting that the same query behaves differently
// depending on which client wrote it.
//
// Neither corpus reaches the node's parser. That is what the live run below does.

const queryCasesWhenWritten = 30

type queryCase struct {
	Name       string                     `json:"name"`
	Build      map[string]json.RawMessage `json:"build"`
	Script     string                     `json:"script"`
	Parameters map[string]json.RawMessage `json:"parameters"`
	Refused    *struct {
		Reason string `json:"reason"`
		What   string `json:"what"`
		Name   string `json:"name"`
	} `json:"refused"`
}

func queryCases(t *testing.T) []queryCase {
	t.Helper()
	corpus := readCorpus(t, "queries-v1.json")
	var cases []queryCase
	if err := json.Unmarshal(corpus["cases"], &cases); err != nil {
		t.Fatalf("the query corpus cases: %v", err)
	}
	if len(cases) < queryCasesWhenWritten {
		t.Fatalf("the corpus carried %d cases when this was written and now carries %d",
			queryCasesWhenWritten, len(cases))
	}
	return cases
}

// A statement, or the refusal that stands in place of one.
type built interface{ Render() (Rendered, error) }

func operatorOf(t *testing.T, name string) Operator {
	t.Helper()
	switch name {
	case "eq":
		return Eq
	case "ne":
		return Ne
	case "lt":
		return Lt
	case "le":
		return Le
	case "gt":
		return Gt
	case "ge":
		return Ge
	}
	t.Fatalf("the corpus used an operator this reader does not know: %s", name)
	return Eq
}

func filterOf(t *testing.T, raw json.RawMessage) Filter {
	t.Helper()
	tag, body := one(t, raw)
	switch tag {
	case "compare":
		var c struct {
			Field string          `json:"field"`
			Op    string          `json:"op"`
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(body, &c); err != nil {
			t.Fatalf("a comparison: %v", err)
		}
		return Compare(c.Field, operatorOf(t, c.Op), valueOf(t, c.Value))
	case "and", "or":
		var pair []json.RawMessage
		if err := json.Unmarshal(body, &pair); err != nil || len(pair) != 2 {
			t.Fatalf("a %s carries two filters: %v", tag, err)
		}
		left, right := filterOf(t, pair[0]), filterOf(t, pair[1])
		if tag == "and" {
			return And(left, right)
		}
		return Or(left, right)
	}
	t.Fatalf("the corpus used a filter shape this reader does not know: %s", tag)
	return nil
}

func selectOf(t *testing.T, body json.RawMessage) built {
	t.Helper()
	var s struct {
		From   string            `json:"from"`
		Fields []json.RawMessage `json:"fields"`
		Where  json.RawMessage   `json:"where"`
		Order  [][]string        `json:"order"`
		Start  *uint64           `json:"start"`
		Limit  *uint64           `json:"limit"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("a select: %v", err)
	}
	statement := Select(s.From)
	for _, field := range s.Fields {
		var name string
		if err := json.Unmarshal(field, &name); err == nil {
			statement.Field(name)
			continue
		}
		var window struct {
			Lines struct {
				Field string `json:"field"`
				Start uint64 `json:"start"`
				Count uint64 `json:"count"`
			} `json:"lines"`
		}
		if err := json.Unmarshal(field, &window); err != nil {
			t.Fatalf("a projection item: %v", err)
		}
		statement.Lines(window.Lines.Field, window.Lines.Start, window.Lines.Count)
	}
	if s.Where != nil {
		statement.Where(filterOf(t, s.Where))
	}
	for _, o := range s.Order {
		direction := Ascending
		if o[1] == "desc" {
			direction = Descending
		}
		statement.OrderBy(o[0], direction)
	}
	if s.Start != nil {
		statement.Start(*s.Start)
	}
	if s.Limit != nil {
		statement.Limit(*s.Limit)
	}
	return statement
}

// The set arrives as an unordered JSON object and is applied in Go's randomised
// map order on purpose: the renderer sorts, so an insertion order that differs
// run to run must still produce the same text and the same numbering.
func setOf(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var set map[string]json.RawMessage
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("a set: %v", err)
	}
	return set
}

func statementOf(t *testing.T, build map[string]json.RawMessage) built {
	t.Helper()
	if len(build) != 1 {
		t.Fatalf("a corpus build carries one statement, got %d", len(build))
	}
	for tag, body := range build {
		switch tag {
		case "select":
			return selectOf(t, body)
		case "create_record", "update_record", "delete_record":
			var w struct {
				Table string          `json:"table"`
				ID    json.RawMessage `json:"id"`
				Set   json.RawMessage `json:"set"`
			}
			if err := json.Unmarshal(body, &w); err != nil {
				t.Fatalf("a %s: %v", tag, err)
			}
			id := valueOf(t, w.ID)
			if tag == "delete_record" {
				return DeleteRecord(w.Table, id)
			}
			if tag == "create_record" {
				statement := CreateRecord(w.Table, id)
				for name, value := range setOf(t, w.Set) {
					statement.Set(name, valueOf(t, value))
				}
				return statement
			}
			statement := UpdateRecord(w.Table, id)
			for name, value := range setOf(t, w.Set) {
				statement.Set(name, valueOf(t, value))
			}
			return statement
		case "create_in_table":
			var w struct {
				Table string          `json:"table"`
				Set   json.RawMessage `json:"set"`
			}
			if err := json.Unmarshal(body, &w); err != nil {
				t.Fatalf("a create_in_table: %v", err)
			}
			statement := CreateInTable(w.Table)
			for name, value := range setOf(t, w.Set) {
				statement.Set(name, valueOf(t, value))
			}
			return statement
		default:
			t.Fatalf("the corpus used a statement this reader does not know: %s", tag)
		}
	}
	return nil
}

func TestEveryQueryCorpusCaseRendersOrIsRefusedExactly(t *testing.T) {
	cases := queryCases(t)
	rendered, refused := 0, 0
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			result, err := statementOf(t, c.Build).Render()

			if c.Refused != nil {
				refused++
				if err == nil {
					t.Fatalf("this case must be refused, and it rendered %q", result.Script)
				}
				if result.Script != "" || result.Parameters != nil {
					t.Fatalf("a refusal renders nothing, and this one produced %q", result.Script)
				}
				refusal, ok := err.(*BuilderError)
				if !ok {
					t.Fatalf("a refusal is a *BuilderError, got %T: %v", err, err)
				}
				if string(refusal.Reason) != c.Refused.Reason {
					t.Fatalf("reason: want %q, got %q", c.Refused.Reason, refusal.Reason)
				}
				if c.Refused.What != "" && refusal.What != c.Refused.What {
					t.Fatalf("position: want %q, got %q", c.Refused.What, refusal.What)
				}
				if c.Refused.Name != "" && refusal.Name != c.Refused.Name {
					t.Fatalf("name: want %q, got %q", c.Refused.Name, refusal.Name)
				}
				return
			}

			rendered++
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if result.Script != c.Script {
				t.Fatalf("script:\n want %q\n  got %q", c.Script, result.Script)
			}
			if len(result.Parameters) != len(c.Parameters) {
				t.Fatalf("parameters: want %d, got %d", len(c.Parameters), len(result.Parameters))
			}
			for name, raw := range c.Parameters {
				got, present := result.Parameters[name]
				if !present {
					t.Fatalf("parameter %s was not bound", name)
				}
				if want := valueOf(t, raw); !sameValue(want, got) {
					t.Fatalf("parameter %s: want %#v, got %#v", name, want, got)
				}
			}
		})
	}
	t.Logf("query corpus: %d cases — %d rendered, %d refused", len(cases), rendered, refused)
}
