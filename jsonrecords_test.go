package tessaridb

import (
	"encoding/json"
	"testing"
)

// Comparing a records outcome field by field against the corpus, rather than by
// re-reading the corpus with the reader under test — which would compare the
// reader with itself and pass for any consistent misreading.

type wantPlan struct {
	Access  string  `json:"access"`
	Source  string  `json:"source"`
	Table   string  `json:"table"`
	Index   string  `json:"index"`
	Shape   string  `json:"shape"`
	Columns *int64  `json:"columns"`
	Cells   *int64  `json:"cells"`
	AtMost  *int64  `json:"at_most"`
	Exact   *bool   `json:"exact"`
	Inexact *string `json:"inexact"`
}

type wantRecords struct {
	Plan  wantPlan          `json:"plan"`
	Rows  []json.RawMessage `json:"rows"`
	Notes []Note            `json:"notes"`
	Only  bool              `json:"only"`
	// A suggestion absent, empty and non-empty are three different claims, so
	// the pointer is doing real work here: nil is "no dictionary was consulted".
	Suggestion *[]Correction `json:"suggestion"`
}

func assertRecords(t *testing.T, want json.RawMessage, got ScriptRecords) {
	t.Helper()
	var expected wantRecords
	if err := json.Unmarshal(want, &expected); err != nil {
		t.Fatalf("the expected records outcome: %v", err)
	}

	assertPlan(t, expected.Plan, got.Plan)

	// path and plan.access are rendered from one field and cannot disagree.
	if got.Path != got.Plan.Access {
		t.Fatalf("path %q and plan.access %q disagree", got.Path, got.Plan.Access)
	}
	if len(expected.Rows) != len(got.Rows) {
		t.Fatalf("rows: want %d, got %d", len(expected.Rows), len(got.Rows))
	}
	for i, row := range expected.Rows {
		var pair struct {
			ID    json.RawMessage `json:"id"`
			Value json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(row, &pair); err != nil {
			t.Fatal(err)
		}
		if identity := identityText(t, pair.ID); identity != got.Rows[i].ID {
			t.Fatalf("row %d id: want %q, got %q", i, identity, got.Rows[i].ID)
		}
		if value := valueOf(t, pair.Value); !sameValue(value, got.Rows[i].Value) {
			t.Fatalf("row %d value: want %#v, got %#v", i, value, got.Rows[i].Value)
		}
	}

	if len(expected.Notes) != len(got.Notes) {
		t.Fatalf("notes: want %d, got %d", len(expected.Notes), len(got.Notes))
	}
	for i, note := range expected.Notes {
		if note != got.Notes[i] {
			t.Fatalf("note %d: want %#v, got %#v", i, note, got.Notes[i])
		}
	}
	if expected.Only != got.Only {
		t.Fatalf("only: want %v, got %v", expected.Only, got.Only)
	}
	assertSuggestion(t, expected.Suggestion, got.Suggest)
}

func assertPlan(t *testing.T, want wantPlan, got Plan) {
	t.Helper()
	for _, field := range []struct {
		name      string
		want, got string
	}{
		{"access", want.Access, got.Access},
		{"source", want.Source, got.Source},
		{"table", want.Table, got.Table},
		{"index", want.Index, got.Index},
		{"shape", want.Shape, got.Shape},
	} {
		if field.want != field.got {
			t.Fatalf("plan.%s: want %q, got %q", field.name, field.want, field.got)
		}
	}
	for _, field := range []struct {
		name      string
		want, got *int64
	}{
		{"columns", want.Columns, got.Columns},
		{"cells", want.Cells, got.Cells},
		{"at_most", want.AtMost, got.AtMost},
	} {
		switch {
		case field.want == nil && field.got == nil:
		case field.want == nil || field.got == nil:
			t.Fatalf("plan.%s: want %v, got %v", field.name, field.want, field.got)
		case *field.want != *field.got:
			t.Fatalf("plan.%s: want %d, got %d", field.name, *field.want, *field.got)
		}
	}

	// The three states, and the third is the one a client loses: no exact key at
	// all is a node that made no claim, and reading that as "exact" puts words
	// in its mouth on the one property whose purpose is that nobody has to infer
	// it.
	switch {
	case want.Exact == nil:
		if got.Exact.State != Unstated {
			t.Fatalf("exactness: want unstated, got %#v", got.Exact)
		}
	case *want.Exact:
		if got.Exact.State != Exact {
			t.Fatalf("exactness: want exact, got %#v", got.Exact)
		}
	default:
		if got.Exact.State != Inexact {
			t.Fatalf("exactness: want inexact, got %#v", got.Exact)
		}
		if want.Inexact == nil || *want.Inexact != got.Exact.Reason {
			t.Fatalf("inexact reason: want %v, got %q", want.Inexact, got.Exact.Reason)
		}
	}
}

func assertSuggestion(t *testing.T, want *[]Correction, got Suggestion) {
	t.Helper()
	if want == nil {
		if got.State != NotConsulted {
			t.Fatalf("suggestion: want not-consulted, got %#v", got)
		}
		return
	}
	if len(*want) == 0 {
		if got.State != Complete {
			t.Fatalf("suggestion: want complete, got %#v", got)
		}
		return
	}
	if got.State != Corrections {
		t.Fatalf("suggestion: want corrections, got %#v", got)
	}
	if len(*want) != len(got.Corrections) {
		t.Fatalf("corrections: want %d, got %d", len(*want), len(got.Corrections))
	}
	for i, correction := range *want {
		if correction != got.Corrections[i] {
			t.Fatalf("correction %d: want %#v, got %#v", i, correction, got.Corrections[i])
		}
	}
}
