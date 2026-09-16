package tessaridb

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Reading the answer to POST /script (§5.6).
//
// These are the same outcomes §3.5 specifies for the wire — the same statements
// produce the same outcomes on both transports — but they are separate types
// here because the two encodings carry different things. The wire's records
// outcome carries a names block and an identity as text; this one carries a
// PLAN, and an identity whose table the statement already named. Folding them
// into one type would put a field on each transport that the other never fills.

// A Reading is what the caller supplies because the JSON cannot carry it: the
// kind of the value. §5.7 states the bargain — this surface is lossy by
// construction and a caller that needs types brings them from the catalog.
//
// It carries nothing for an identity, deliberately. §5.6 and §5.7.1 both say a
// client MUST NOT parse one back into a typed record id, and the corpus's own
// keys case is the argument: it holds "1" and "ada" in ONE array, an integer
// identity beside a text one, which no single supplied kind could express. An
// identity here is a string to display, log and pass back, exactly as the wire's
// Row.Identity is; a caller that needs the type reads it from §4, where it
// carries its tag.
type Reading struct {
	// Value is the kind of a value outcome, or of each row's value.
	Value Kind
	Names Names
}

// A ScriptOutcome is one statement's result on the HTTP surface.
type ScriptOutcome interface{ isScriptOutcome() }

type ScriptDone struct{}

// ScriptValue carries None for a value outcome with no value key. That is the
// language's none, and it is NOT the same as a value key holding null — JSON has
// one word for both, so the distinction rides on the presence of the key.
type ScriptValue struct{ Value Value }

type ScriptRecords struct {
	Path    string
	Plan    Plan
	Rows    []ScriptRow
	Notes   []Note
	Only    bool
	Suggest Suggestion
}

// ScriptKeys holds identities as the id half alone — no table prefix and no
// colon, because the statement named the table and the answer does not repeat
// it.
type ScriptKeys struct{ Keys []string }

type ScriptRemoved struct{ Count uint64 }

// ScriptUnknown is a kind this build has never seen, carrying the word the node
// used. Surfaced rather than dropped, and reading does not stop at it: it means
// an outcome this build cannot read, which points at upgrading rather than at
// shrugging.
type ScriptUnknown struct{ Kind string }

func (ScriptDone) isScriptOutcome()    {}
func (ScriptValue) isScriptOutcome()   {}
func (ScriptRecords) isScriptOutcome() {}
func (ScriptKeys) isScriptOutcome()    {}
func (ScriptRemoved) isScriptOutcome() {}
func (ScriptUnknown) isScriptOutcome() {}

type ScriptRow struct {
	ID    string
	Value Value
}

// Plan is how the read reached its records. Access is always present and always
// equals the outcome's Path; every other key is absent when the read had no
// answer for it, rather than present holding null.
//
// The three counts are pointers because zero is a legitimate count, so absence
// cannot be spelled 0. Source and Shape are OPEN word sets — render an
// unrecognised one rather than switching on it exhaustively — while Access is
// the closed set, and the difference matters because they look alike in the same
// object.
type Plan struct {
	Access  string
	Source  string
	Table   string
	Index   string
	Shape   string
	Columns *int64
	Cells   *int64
	AtMost  *int64
	// Exact is the one plan key that is always written, so that its absence can
	// mean a node that predates the field rather than the dull value. A key
	// present only when interesting teaches a client that absence means "exact",
	// and here "exact" is a claim.
	Exact Exactness
}

// ScriptResults reads the answer body and returns one raw outcome per statement,
// in order. The caller reads each with the Reading its own statement implies,
// which is why this stops at the list rather than interpreting it: a script's
// statements need not share a kind.
func ScriptResults(body []byte) ([]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var answer struct {
		Results []any `json:"results"`
	}
	if err := decoder.Decode(&answer); err != nil {
		return nil, fmt.Errorf("tessaridb: the answer body is not JSON: %w", err)
	}
	return answer.Results, nil
}

func ReadOutcome(j any, reading Reading) (ScriptOutcome, error) {
	object, ok := j.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tessaridb: an outcome is a JSON object, got %T", j)
	}
	kind, err := jsonText(object["kind"], "an outcome's kind")
	if err != nil {
		return nil, err
	}

	switch kind {
	case "done":
		return ScriptDone{}, nil

	case "value":
		raw, present := object["value"]
		if !present {
			return ScriptValue{Value: None{}}, nil
		}
		value, err := Interpret(raw, reading.Value, reading.Names)
		return ScriptValue{Value: value}, err

	case "removed":
		n, err := jsonNumber(object["count"], "a removed count")
		if err != nil {
			return nil, err
		}
		count, err := n.Int64()
		if err != nil || count < 0 {
			return nil, fmt.Errorf("tessaridb: %q is not a removed count", n)
		}
		return ScriptRemoved{Count: uint64(count)}, nil

	case "keys":
		raw, ok := object["keys"].([]any)
		if !ok {
			return nil, fmt.Errorf("tessaridb: a keys outcome carries an array")
		}
		keys := make([]string, 0, len(raw))
		for _, each := range raw {
			identity, err := jsonText(each, "a record identity")
			if err != nil {
				return nil, err
			}
			keys = append(keys, identity)
		}
		return ScriptKeys{Keys: keys}, nil

	case "records":
		return scriptRecords(object, reading)
	}
	// Any word this build does not know, including "unknown" itself.
	return ScriptUnknown{Kind: kind}, nil
}

func scriptRecords(object map[string]any, reading Reading) (ScriptOutcome, error) {
	path, err := jsonText(object["path"], "an access path")
	if err != nil {
		return nil, err
	}
	plan, err := scriptPlan(object["plan"])
	if err != nil {
		return nil, err
	}

	raw, ok := object["records"].([]any)
	if !ok {
		return nil, fmt.Errorf("tessaridb: a records outcome carries an array")
	}
	rows := make([]ScriptRow, 0, len(raw))
	for _, each := range raw {
		pair, ok := each.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tessaridb: a record is a pair of an id and a value")
		}
		id, err := jsonText(pair["id"], "a record identity")
		if err != nil {
			return nil, err
		}
		value, err := Interpret(pair["value"], reading.Value, reading.Names)
		if err != nil {
			return nil, err
		}
		rows = append(rows, ScriptRow{ID: id, Value: value})
	}

	notes, err := scriptNotes(object["notes"])
	if err != nil {
		return nil, err
	}
	only, _ := object["only"].(bool)
	suggest, err := scriptSuggestion(object["suggestion"])
	if err != nil {
		return nil, err
	}
	return ScriptRecords{
		Path: path, Plan: plan, Rows: rows,
		Notes: notes, Only: only, Suggest: suggest,
	}, nil
}

func scriptNotes(j any) ([]Note, error) {
	if j == nil {
		return nil, nil
	}
	raw, ok := j.([]any)
	if !ok {
		return nil, fmt.Errorf("tessaridb: notes are an array")
	}
	notes := make([]Note, 0, len(raw))
	for _, each := range raw {
		object, ok := each.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tessaridb: a note is an object")
		}
		kind, err := jsonText(object["kind"], "a note's kind")
		if err != nil {
			return nil, err
		}
		message, err := jsonText(object["message"], "a note's message")
		if err != nil {
			return nil, err
		}
		notes = append(notes, Note{Kind: kind, Message: message})
	}
	return notes, nil
}

// Three states from two JSON facts. The key absent is the absence of a claim —
// no term dictionary was consulted — and present-and-empty IS the claim, that
// one was consulted and holds every term the query named. This is the one place
// on this transport where an absent key does not mean the dull value, and a
// client that reads it as "nothing is near" reports a negative nobody checked.
func scriptSuggestion(j any) (Suggestion, error) {
	if j == nil {
		return Suggestion{State: NotConsulted}, nil
	}
	object, ok := j.(map[string]any)
	if !ok {
		return Suggestion{}, fmt.Errorf("tessaridb: a suggestion is an object")
	}
	raw, ok := object["corrections"].([]any)
	if !ok {
		return Suggestion{}, fmt.Errorf("tessaridb: a suggestion carries corrections")
	}
	if len(raw) == 0 {
		return Suggestion{State: Complete}, nil
	}
	corrections := make([]Correction, 0, len(raw))
	for _, each := range raw {
		object, ok := each.(map[string]any)
		if !ok {
			return Suggestion{}, fmt.Errorf("tessaridb: a correction is an object")
		}
		typed, err := jsonText(object["typed"], "a correction's typed term")
		if err != nil {
			return Suggestion{}, err
		}
		instead, err := jsonText(object["instead"], "a correction's replacement")
		if err != nil {
			return Suggestion{}, err
		}
		corrections = append(corrections, Correction{Typed: typed, Instead: instead})
	}
	return Suggestion{State: Corrections, Corrections: corrections}, nil
}
