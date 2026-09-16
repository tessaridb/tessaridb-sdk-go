package tessaridb

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// How the scalar spellings of §5.7.1 are read back. A one-line table says which
// JSON type a value takes and cannot say how the value is written inside it;
// these five rows are the second half, and a client that guesses it guesses
// wrong in ways nothing reports.

// Numbers arrive as json.Number because the decoder is given UseNumber. This is
// the whole of the i64 problem on this transport: a record identity is an i64
// and a plain decode reads every JSON number into a float64, so an id past 2^53
// comes back CHANGED with nothing anywhere reporting it. Keeping the literal
// text and parsing it at the declared kind costs nothing and loses nothing.
func jsonNumber(j any, what string) (json.Number, error) {
	n, ok := j.(json.Number)
	if !ok {
		return "", fmt.Errorf("tessaridb: %s is a JSON jsonNumber, got %T", what, j)
	}
	return n, nil
}

func jsonText(j any, what string) (string, error) {
	s, ok := j.(string)
	if !ok {
		return "", fmt.Errorf("tessaridb: %s is a JSON string, got %T", what, j)
	}
	return s, nil
}

// The canonical quiet NaN, and NOT math.NaN().
//
// Go's math.NaN() is Float64frombits(0x7FF8000000000001) — a quiet NaN with a
// payload of 1. The protocol's canonical quiet NaN carries no payload, so a
// client that reads "NaN" into math.NaN() holds a value that RE-ENCODES to
// different bytes than the one the node sent, and every check it makes about
// itself agrees with itself. The corpus is what catches it.
const canonicalNaN = 0x7ff8000000000000

func jsonFloat(j any) (float64, error) {
	if n, ok := j.(json.Number); ok {
		return strconv.ParseFloat(n.String(), 64)
	}
	// JSON has no spelling for a non-finite, so the surface quotes it.
	switch j {
	case "inf":
		return math.Inf(1), nil
	case "-inf":
		return math.Inf(-1), nil
	case "NaN":
		return math.Float64frombits(canonicalNaN), nil
	}
	return 0, fmt.Errorf("tessaridb: a float is a jsonNumber or a quoted non-finite, got %v", j)
}

// A coordinate may be null, which is how a non-finite one is written.
func jsonCoordinate(j any) (float64, error) {
	if j == nil {
		return math.Float64frombits(canonicalNaN), nil
	}
	n, err := jsonNumber(j, "a coordinate")
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(n.String(), 64)
}

// "12.34" is a mantissa of 1234 and a scale of 2. A JSON number is a double in
// every parser that matters, which is why this row is a string at all.
func jsonDecimal(source string) (Value, error) {
	body, fraction, dotted := strings.Cut(source, ".")
	digits := body + fraction
	if digits == "" || (dotted && fraction == "") {
		return nil, fmt.Errorf("tessaridb: %q is not a decimal", source)
	}
	mantissa, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, fmt.Errorf("tessaridb: %q is not a decimal", source)
	}
	return Decimal{Mantissa: mantissa, Scale: uint32(len(fraction))}, nil
}

const nanosPerSecond = 1_000_000_000

// Units largest first, and the two-character units before the one-character ones
// they start with — otherwise "500ms" reads as 500 minutes followed by a stray s.
var durationUnits = []struct {
	suffix  string
	seconds int64
	nanos   int64
}{
	{"h", 3600, 0},
	{"ms", 0, 1_000_000},
	{"m", 60, 0},
	{"s", 1, 0},
	{"us", 0, 1_000},
	{"ns", 0, 1},
}

// A duration is a decomposition over exactly six units, largest first, with a
// zero count omitted; a zero span is "0s" because an empty string is not a
// literal. A span below zero is a leading '-' and then the magnitude.
//
// The store carries whole seconds plus nanoseconds in [0, 1e9), so a negative
// span is a SMALLER second count and a positive remainder: -500ms is
// -1s + 500000000ns. Truncating division would give 0s and a negative
// remainder, which is a different duration and a legal-looking one.
func jsonDuration(source string) (Value, error) {
	body, negative := strings.CutPrefix(source, "-")
	if body == "" {
		return nil, fmt.Errorf("tessaridb: %q is not a duration", source)
	}

	var seconds, nanos int64
	for body != "" {
		width := 0
		for width < len(body) && body[width] >= '0' && body[width] <= '9' {
			width++
		}
		if width == 0 {
			return nil, fmt.Errorf("tessaridb: %q is not a duration", source)
		}
		count, err := strconv.ParseInt(body[:width], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("tessaridb: %q carries a count this client cannot hold", source)
		}
		rest := body[width:]

		matched := false
		for _, unit := range durationUnits {
			if !strings.HasPrefix(rest, unit.suffix) {
				continue
			}
			// Guarded rather than multiplied: a wrapped product here is a
			// duration that is wrong by years and raises nothing.
			if unit.seconds != 0 {
				if count > math.MaxInt64/unit.seconds {
					return nil, fmt.Errorf("tessaridb: %q does not fit an i64 of seconds", source)
				}
				seconds += count * unit.seconds
			} else {
				nanos += count * unit.nanos
			}
			body = rest[len(unit.suffix):]
			matched = true
			break
		}
		if !matched {
			return nil, fmt.Errorf("tessaridb: %q names a unit this client does not know", source)
		}
	}

	seconds += nanos / nanosPerSecond
	nanos %= nanosPerSecond
	if negative {
		seconds, nanos = -seconds, -nanos
	}
	if nanos < 0 {
		seconds--
		nanos += nanosPerSecond
	}
	return Duration{Seconds: seconds, Nanos: uint32(nanos)}, nil
}

// RFC 3339, always UTC and always ending in Z: an offset supplied on input is
// applied and then discarded, so an answer never carries one and the caller's
// zone is not recoverable. Up to nine fractional digits, trailing zeros trimmed,
// so ".5" is 500000000ns rather than 5.
func jsonDatetime(source string) (Value, error) {
	at, err := time.Parse(time.RFC3339Nano, source)
	if err != nil {
		return nil, fmt.Errorf("tessaridb: %q is not an RFC 3339 datetime", source)
	}
	return Datetime{Seconds: at.Unix(), Nanos: uint32(at.Nanosecond())}, nil
}

func jsonUUID(source string) (Value, error) {
	raw, err := jsonUUIDBytes(source)
	if err != nil {
		return nil, err
	}
	return UUID{Value: raw}, nil
}

func jsonUUIDBytes(source string) ([16]byte, error) {
	var out [16]byte
	// Hyphenated as a value, unhyphenated inside a record id — the same sixteen
	// bytes written in two syntaxes, which is why a client that reuses its uuid
	// parser on the id half does not find hyphens where it expects them.
	raw, err := hex.DecodeString(strings.ReplaceAll(source, "-", ""))
	if err != nil || len(raw) != 16 {
		return out, fmt.Errorf("tessaridb: %q is not a uuid", source)
	}
	copy(out[:], raw)
	return out, nil
}
