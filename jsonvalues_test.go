package tessaridb

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The JSON value corpus, §5.7.
//
// This one is decode-only, and the corpus says why: a client never encodes a
// value on this surface, because a POST /script parameter carries TessariQL
// source rather than JSON (§5.5). So it is weaker than the value corpus by
// construction — it cannot catch a reader and a writer that are wrong in the
// same way, because there is no writer.
//
// What it does catch is every place the JSON is lossy and the kind is what
// restores the value: "users:7" is the integer 7 or the text '7' depending only
// on what the caller was told, and both cases are here.

const jsonCasesWhenWritten = 59

// float-negative-zero cannot be recovered and is not a defect in this client.
// §5.7.1: a float is normalised when the value is built, so -0.0 and 0.0 are one
// value and it writes as 0. The corpus carries the case to make a client SAY so
// rather than quietly reporting a sign it did not receive.
const unrecoverable = "float-negative-zero"

type jsonCase struct {
	Name    string          `json:"name"`
	JSON    json.RawMessage `json:"json"`
	Omitted bool            `json:"omitted"`
	Value   json.RawMessage `json:"value"`
}

// decodeJSON is how a client reads a body on this surface: UseNumber, so a
// number keeps its literal text. Without it every JSON number becomes a float64
// and a record identity past 2^53 comes back changed with nothing reporting it.
func decodeJSON(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out any
	if err := decoder.Decode(&out); err != nil {
		t.Fatalf("the corpus json: %v", err)
	}
	return out
}

func jsonNames(t *testing.T, corpus map[string]json.RawMessage) Names {
	t.Helper()
	var block map[string]string
	if err := json.Unmarshal(corpus["names"], &block); err != nil {
		t.Fatalf("the names block: %v", err)
	}
	names := make(Names, len(block))
	for id, name := range block {
		names[name] = uint32(num(t, json.RawMessage(id)))
	}
	return names
}

// The kind a caller would have read from the catalog, derived here from the
// value the corpus says the JSON means. That is the bargain §5.7 states: the
// JSON does not carry the type and the caller supplies it, so a corpus that
// withheld it could not describe this surface at all.
func kindOf(t *testing.T, raw json.RawMessage) Kind {
	t.Helper()
	tag, body := one(t, raw)
	switch tag {
	case "none", "null":
		return NullKind{}
	case "bool":
		return BoolKind{}
	case "integer":
		return IntegerKind{}
	case "float_bits":
		return FloatKind{}
	case "decimal":
		return DecimalKind{}
	case "string":
		return StringKind{}
	case "bytes":
		return BytesKind{}
	case "duration":
		return DurationKind{}
	case "datetime":
		return DatetimeKind{}
	case "uuid":
		return UUIDKind{}
	case "table":
		return TableKind{}
	case "regex":
		return RegexKind{}
	case "geometry":
		return GeometryKind{}
	case "record":
		var r struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatal(err)
		}
		which, _ := one(t, r.ID)
		switch which {
		case "int":
			return RecordKind{ID: IDInteger}
		case "text":
			return RecordKind{ID: IDText}
		case "uuid":
			return RecordKind{ID: IDUUID}
		case "bytes":
			return RecordKind{ID: IDBytes}
		}
		t.Fatalf("unknown record id tag in the corpus: %s", which)
	case "array", "set":
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			t.Fatal(err)
		}
		each := make([]Kind, 0, len(items))
		for _, item := range items {
			each = append(each, kindOf(t, item))
		}
		if tag == "set" {
			return SetKind{Each: each}
		}
		return ArrayKind{Each: each}
	case "object":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		declared := make(map[string]Kind, len(fields))
		for name, field := range fields {
			declared[name] = kindOf(t, field)
		}
		return ObjectKind{Fields: declared}
	case "range":
		var r struct {
			Start json.RawMessage `json:"start"`
			End   json.RawMessage `json:"end"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			t.Fatal(err)
		}
		// Both ends open carries no value to take a kind from, and needs none.
		for _, end := range []json.RawMessage{r.Start, r.End} {
			if which, inner := one(t, end); which != "unbounded" {
				return RangeKind{Of: kindOf(t, inner)}
			}
		}
		return RangeKind{Of: NullKind{}}
	}
	t.Fatalf("the corpus used a value tag this reader does not know: %s", tag)
	return nil
}

func TestEveryJSONCorpusCaseReadsBackToItsValue(t *testing.T) {
	corpus := readCorpus(t, "json-v1.json")
	names := jsonNames(t, corpus)
	var cases []jsonCase
	if err := json.Unmarshal(corpus["cases"], &cases); err != nil {
		t.Fatalf("the json corpus cases: %v", err)
	}
	if len(cases) < jsonCasesWhenWritten {
		t.Fatalf("the corpus carried %d cases when this was written and now carries %d",
			jsonCasesWhenWritten, len(cases))
	}

	recovered, named := 0, 0
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			want := valueOf(t, c.Value)
			var source any = Absent{}
			if !c.Omitted {
				source = decodeJSON(t, c.JSON)
			}
			got, err := Interpret(source, kindOf(t, c.Value), names)

			if c.Name == unrecoverable {
				named++
				if err != nil {
					t.Fatalf("this case is lossy, not unreadable: %v", err)
				}
				if sameValue(want, got) {
					t.Fatalf("this case cannot be recovered from the JSON, " +
						"so a client that reports it recovered is reporting a sign it never received")
				}
				return
			}

			if err != nil {
				t.Fatalf("interpret: %v", err)
			}
			if !sameValue(want, got) {
				t.Fatalf("value:\n want %#v\n  got %#v", want, got)
			}
			recovered++
		})
	}
	t.Logf("json value corpus: %d cases — %d recovered, %d lossy by construction",
		len(cases), recovered, named)
}
