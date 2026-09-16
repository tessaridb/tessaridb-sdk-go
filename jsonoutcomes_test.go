package tessaridb

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"testing"
)

// The 20 outcome shapes of §5.6.
//
// The value corpus proves a value reads back; this proves the SHAPE around it
// does — and most of these cases exist for a distinction carried by the presence
// or absence of a key rather than by its contents. A reader that models those as
// optional fields with defaults passes none of them.
//
// Identities are compared the one direction the specification allows. The corpus
// renders an identity as a TYPED record; §5.6 and §5.7.1 both say a client MUST
// NOT parse the string back into one. Typed to text is well defined, text to
// typed is not — the integer 7 and the text '7' are written identically — so the
// test renders the corpus's identity forward and compares it with the string the
// client kept.

const outcomesWhenWritten = 20

type outcomeCase struct {
	Name    string          `json:"name"`
	JSON    json.RawMessage `json:"json"`
	Outcome json.RawMessage `json:"outcome"`
}

// The Reading a caller would have brought from its own catalog, derived here
// from the outcome the corpus says the JSON means — the same bargain the value
// corpus makes, for the same reason.
func readingOf(t *testing.T, raw json.RawMessage) Reading {
	t.Helper()
	reading := Reading{Value: NullKind{}}
	tag, body := one(t, raw)
	switch tag {
	case "value":
		reading.Value = kindOf(t, body)
	case "records":
		var r struct {
			Rows []json.RawMessage `json:"rows"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatal(err)
		}
		if len(r.Rows) > 0 {
			var row struct {
				Value json.RawMessage `json:"value"`
			}
			if err := json.Unmarshal(r.Rows[0], &row); err != nil {
				t.Fatal(err)
			}
			reading.Value = kindOf(t, row.Value)
		}
	}
	return reading
}

// The id half as §5.7.1 writes it: an integer in decimal, a text id as itself, a
// uuid stripped of its hyphens, and bytes behind a 0x.
func identityText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	tag, body := one(t, raw)
	if tag != "record" {
		t.Fatalf("an identity is a record, got %s", tag)
	}
	var r struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	which, id := one(t, r.ID)
	switch which {
	case "int":
		return strconv.FormatInt(i64(t, id), 10)
	case "text":
		return str(t, id)
	case "uuid":
		return str(t, id)
	case "bytes":
		return "0x" + hex.EncodeToString(bytesOf(t, id))
	}
	t.Fatalf("unknown record id tag: %s", which)
	return ""
}

func TestEveryOutcomeCorpusCaseReadsBackToItsOutcome(t *testing.T) {
	corpus := readCorpus(t, "json-v1.json")
	names := jsonNames(t, corpus)
	var cases []outcomeCase
	if err := json.Unmarshal(corpus["outcomes"], &cases); err != nil {
		t.Fatalf("the outcome corpus: %v", err)
	}
	if len(cases) < outcomesWhenWritten {
		t.Fatalf("the corpus carried %d outcomes when this was written and now carries %d",
			outcomesWhenWritten, len(cases))
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			reading := readingOf(t, c.Outcome)
			reading.Names = names
			got, err := ReadOutcome(decodeJSON(t, c.JSON), reading)
			if err != nil {
				t.Fatalf("read outcome: %v", err)
			}
			assertOutcome(t, c.Outcome, got)
		})
	}
	t.Logf("json outcome corpus: %d shapes", len(cases))
}

func assertOutcome(t *testing.T, want json.RawMessage, got ScriptOutcome) {
	t.Helper()
	tag, body := one(t, want)
	switch tag {
	case "done":
		if _, ok := got.(ScriptDone); !ok {
			t.Fatalf("want done, got %#v", got)
		}
	case "unknown":
		if _, ok := got.(ScriptUnknown); !ok {
			t.Fatalf("want unknown, got %#v", got)
		}
	case "removed":
		outcome, ok := got.(ScriptRemoved)
		if !ok || outcome.Count != uint64(num(t, body)) {
			t.Fatalf("want removed %s, got %#v", body, got)
		}
	case "value":
		outcome, ok := got.(ScriptValue)
		if !ok {
			t.Fatalf("want a value, got %#v", got)
		}
		if expected := valueOf(t, body); !sameValue(expected, outcome.Value) {
			t.Fatalf("value: want %#v, got %#v", expected, outcome.Value)
		}
	case "keys":
		outcome, ok := got.(ScriptKeys)
		if !ok {
			t.Fatalf("want keys, got %#v", got)
		}
		var keys []json.RawMessage
		if err := json.Unmarshal(body, &keys); err != nil {
			t.Fatal(err)
		}
		if len(keys) != len(outcome.Keys) {
			t.Fatalf("keys: want %d, got %d", len(keys), len(outcome.Keys))
		}
		for i, key := range keys {
			if expected := identityText(t, key); expected != outcome.Keys[i] {
				t.Fatalf("key %d: want %q, got %q", i, expected, outcome.Keys[i])
			}
		}
	case "records":
		outcome, ok := got.(ScriptRecords)
		if !ok {
			t.Fatalf("want records, got %#v", got)
		}
		assertRecords(t, body, outcome)
	default:
		t.Fatalf("the corpus used an outcome this reader does not know: %s", tag)
	}
}
