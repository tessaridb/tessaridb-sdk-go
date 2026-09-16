package tessaridb

import (
	"encoding/hex"
	"fmt"
	"strconv"
)

// Interpret reads one value off the HTTP surface, with its kind supplied.
//
// Pass Absent{} for a key that was not in the object: that is the language's
// none, and it is the one case where absence carries meaning rather than
// signalling a mistake.
//
// names resolves a table reference that the answer wrote as a name. A reference
// the node could not resolve is written in the bracketed form instead, and is
// read without consulting names at all.
func Interpret(j any, kind Kind, names Names) (Value, error) {
	if _, absent := j.(Absent); absent {
		return None{}, nil
	}

	switch declared := kind.(type) {
	case NullKind:
		if j != nil {
			return nil, fmt.Errorf("tessaridb: a null, got %T", j)
		}
		return Null{}, nil

	case BoolKind:
		value, ok := j.(bool)
		if !ok {
			return nil, fmt.Errorf("tessaridb: a bool, got %T", j)
		}
		return Bool{Value: value}, nil

	case IntegerKind:
		n, err := jsonNumber(j, "an integer")
		if err != nil {
			return nil, err
		}
		value, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("tessaridb: %q does not fit an i64", n)
		}
		return Integer{Value: value}, nil

	case FloatKind:
		value, err := jsonFloat(j)
		return Float{Value: value}, err

	case DecimalKind:
		source, err := jsonText(j, "a decimal")
		if err != nil {
			return nil, err
		}
		return jsonDecimal(source)

	case StringKind:
		value, err := jsonText(j, "a string")
		return Text{Value: value}, err

	case BytesKind:
		source, err := jsonText(j, "bytes")
		if err != nil {
			return nil, err
		}
		raw, err := hex.DecodeString(source)
		if err != nil {
			return nil, fmt.Errorf("tessaridb: %q is not hex", source)
		}
		return Bytes{Value: raw}, nil

	case DurationKind:
		source, err := jsonText(j, "a duration")
		if err != nil {
			return nil, err
		}
		return jsonDuration(source)

	case DatetimeKind:
		source, err := jsonText(j, "a datetime")
		if err != nil {
			return nil, err
		}
		return jsonDatetime(source)

	case UUIDKind:
		source, err := jsonText(j, "a uuid")
		if err != nil {
			return nil, err
		}
		return jsonUUID(source)

	case RegexKind:
		// Carried, never compiled: Go's RE2 and the store's engine are not the
		// same language, and presenting one's answer as the other's is a lie.
		pattern, err := jsonText(j, "a regex")
		return Regex{Pattern: pattern}, err

	case TableKind:
		source, err := jsonText(j, "a table")
		if err != nil {
			return nil, err
		}
		id, err := jsonTableID(source, names)
		return Table{ID: id}, err

	case RecordKind:
		source, err := jsonText(j, "a record")
		if err != nil {
			return nil, err
		}
		return jsonRecord(source, declared.ID, names)

	case GeometryKind:
		shape, err := jsonGeometry(j)
		return Geometry{Shape: shape}, err

	case ArrayKind:
		items, err := jsonItems(j, declared.Of, declared.Each, names, "an array")
		return Array{Items: items}, err

	case SetKind:
		// A set arrives as an array and the collection type is not recoverable
		// from the JSON — the caller's declared kind is what restores it.
		items, err := jsonItems(j, declared.Of, declared.Each, names, "a set")
		return Set{Items: items}, err

	case ObjectKind:
		object, ok := j.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tessaridb: an object, got %T", j)
		}
		fields := make(map[string]Value, len(declared.Fields))
		for name, of := range declared.Fields {
			raw, present := object[name]
			if !present {
				fields[name] = None{}
				continue
			}
			value, err := Interpret(raw, of, names)
			if err != nil {
				return nil, err
			}
			fields[name] = value
		}
		return Object{Fields: fields}, nil

	case RangeKind:
		object, ok := j.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tessaridb: a range, got %T", j)
		}
		start, err := jsonBound(object["start"], declared.Of, names)
		if err != nil {
			return nil, err
		}
		end, err := jsonBound(object["end"], declared.Of, names)
		return Range{Start: start, End: end}, err
	}
	return nil, fmt.Errorf("tessaridb: %T is not a kind this client can read", kind)
}

func jsonItems(j any, of Kind, each []Kind, names Names, what string) ([]Value, error) {
	raw, ok := j.([]any)
	if !ok {
		return nil, fmt.Errorf("tessaridb: %s, got %T", what, j)
	}
	items := make([]Value, 0, len(raw))
	for i, element := range raw {
		kind := of
		if each != nil {
			if i >= len(each) {
				return nil, fmt.Errorf("tessaridb: %s carries more elements than its kind declares", what)
			}
			kind = each[i]
		}
		value, err := Interpret(element, kind, names)
		if err != nil {
			return nil, err
		}
		items = append(items, value)
	}
	return items, nil
}

// An unbounded end carries no value key at all, which is what keeps an open end
// distinct from an end holding null. 1..5, 1..=5 and 1.. are three different
// spans, and a model without the bound kinds collapses them onto one.
func jsonBound(j any, of Kind, names Names) (Bound, error) {
	object, ok := j.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tessaridb: a range endpoint is an object, got %T", j)
	}
	which, err := jsonText(object["bound"], "a range endpoint's bound")
	if err != nil {
		return nil, err
	}
	if which == "unbounded" {
		return Unbounded{}, nil
	}
	raw, present := object["value"]
	if !present {
		return nil, fmt.Errorf("tessaridb: an %s endpoint carries a value", which)
	}
	value, err := Interpret(raw, of, names)
	if err != nil {
		return nil, err
	}
	switch which {
	case "included":
		return Included{Value: value}, nil
	case "excluded":
		return Excluded{Value: value}, nil
	}
	return nil, fmt.Errorf("tessaridb: %q is not included, excluded or unbounded", which)
}
