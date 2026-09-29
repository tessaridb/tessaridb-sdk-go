package tessaridb

import (
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
)

// How an event's values are spelled for POST /series (§5.9). Offline: the node
// is the oracle for these spellings and the live test asks it; this pins each
// one so a change to the renderer is seen here first.
func TestEachKindAnEventCarriesHasItsSpelling(t *testing.T) {
	uuid := UUID{Value: [16]byte{0x01, 0x90, 0xa0, 0xb1, 0, 0, 0x70, 0, 0x80, 0, 0, 0, 0, 0, 0, 1}}
	for _, test := range []struct {
		name  string
		value Value
		want  string
	}{
		{"null", Null{}, "NULL"},
		{"bool", Bool{Value: true}, "true"},
		{"integer", Integer{Value: -12}, "-12"},
		{"whole float", Float{Value: 1}, "1.0"},
		{"large float", Float{Value: 1.5e300}, "1.5e+300"},
		{"decimal", Decimal{Mantissa: big.NewInt(-1234), Scale: 2}, "dec -12.34"},
		{"small decimal", Decimal{Mantissa: big.NewInt(5), Scale: 3}, "dec 0.005"},
		{"string", Text{Value: `it's \ ok`}, `'it\'s \\ ok'`},
		{"datetime", Datetime{Seconds: 1_790_676_000, Nanos: 123_456_789}, "datetime '2026-09-29T10:00:00.123456789Z'"},
		{"before the epoch", Datetime{Seconds: -1}, "datetime '1969-12-31T23:59:59Z'"},
		{"uuid", uuid, "uuid '0190a0b1-0000-7000-8000-000000000001'"},
		{"object", Object{Fields: map[string]Value{
			"odd key": Array{Items: []Value{Bool{Value: false}}},
			"gone":    None{},
		}}, "{ 'odd key': [false] }"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out strings.Builder
			if err := eventLiteral(test.value, &out); err != nil {
				t.Fatalf("%v", err)
			}
			if out.String() != test.want {
				t.Fatalf("got %s, want %s", out.String(), test.want)
			}
		})
	}
}

func TestAKindAnEventCannotCarryIsRefusedBeforeAnythingIsSent(t *testing.T) {
	for _, test := range []struct {
		name  string
		value Value
	}{
		{"not finite", Float{Value: math.NaN()}},
		{"bytes", Bytes{Value: []byte{1}}},
		{"absence in an array", Array{Items: []Value{None{}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out strings.Builder
			var refused *NotAnEventError
			if err := eventLiteral(test.value, &out); !errors.As(err, &refused) {
				t.Fatalf("want NotAnEventError, got %v", err)
			}
		})
	}
	var refused *NotAnEventError
	if _, err := eventBatch([]Value{Bool{Value: true}}); !errors.As(err, &refused) {
		t.Fatalf("an event is an object, got %v", err)
	}
}
