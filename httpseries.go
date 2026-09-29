package tessaridb

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The series route (§5.9): a batch of events appended in ONE transaction.
//
// The body is one TessariQL value — an array of objects of literals — so each
// value is rendered as TessariQL source, which §5.9 makes this client's job. The
// kinds an event needs each have one spelling; every other kind is refused here,
// before a byte is sent, rather than approximated into a value nobody meant.

// NotAnEventError is a batch holding something an event cannot carry. Nothing
// was sent.
type NotAnEventError struct{ Reason string }

func (e *NotAnEventError) Error() string { return "not an event: " + e.Reason }

// Append writes a batch of events to a series in one transaction and answers how
// many landed. Every event is an Object; a field holding None is left out.
//
// The batch lands whole or not at all, and it is NOT idempotent: sent twice it
// lands twice. So it is sent once — a transport failure after the request left
// may mean it landed, and only the caller knows whether a second copy would be
// harmless. The one resend is after a 401 on a lapsed token, which the node
// answers before running anything.
func (c *HTTPClient) Append(namespace, database, series string, events []Value) (uint64, error) {
	body, err := eventBatch(events)
	if err != nil {
		return 0, err
	}
	path := "/series/" + namespace + "/" + database + "/" + series
	response, err := c.send(http.MethodPost, path, []byte(body), "text/plain")
	if err != nil {
		return 0, err
	}
	if response.StatusCode != http.StatusOK {
		return 0, refusal(response)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, fmt.Errorf("reading an append's answer: %w", err)
	}
	var answer struct {
		Appended *uint64 `json:"appended"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil || answer.Appended == nil {
		return 0, fmt.Errorf("an append must say how many landed: %s", raw)
	}
	return *answer.Appended, nil
}

func eventBatch(events []Value) (string, error) {
	var out strings.Builder
	out.WriteByte('[')
	for position, event := range events {
		if _, ok := event.(Object); !ok {
			return "", &NotAnEventError{Reason: "an event is an object"}
		}
		if position > 0 {
			out.WriteString(", ")
		}
		if err := eventLiteral(event, &out); err != nil {
			return "", err
		}
	}
	out.WriteByte(']')
	return out.String(), nil
}

// eventLiteral spells one value as §5.9 spells it.
func eventLiteral(value Value, out *strings.Builder) error {
	switch held := value.(type) {
	case Null:
		out.WriteString("NULL")
	case Bool:
		out.WriteString(strconv.FormatBool(held.Value))
	case Integer:
		out.WriteString(strconv.FormatInt(held.Value, 10))
	case Float:
		if math.IsInf(held.Value, 0) || math.IsNaN(held.Value) {
			return &NotAnEventError{Reason: "a float that is not finite has no spelling"}
		}
		// A float must carry a `.` or an exponent, or it reads as an integer.
		text := strconv.FormatFloat(held.Value, 'g', -1, 64)
		if !strings.ContainsAny(text, ".e") {
			text += ".0"
		}
		out.WriteString(text)
	case Decimal:
		out.WriteString(eventDecimal(held))
	case Text:
		eventQuoted(held.Value, out)
	case Datetime:
		moment := time.Unix(held.Seconds, 0).UTC()
		if moment.Year() < 0 || moment.Year() > 9999 {
			return &NotAnEventError{Reason: "a datetime outside the years 0 to 9999"}
		}
		out.WriteString("datetime '" + moment.Format("2006-01-02T15:04:05"))
		if held.Nanos > 0 {
			fmt.Fprintf(out, ".%09d", held.Nanos)
		}
		out.WriteString("Z'")
	case UUID:
		hexed := hex.EncodeToString(held.Value[:])
		out.WriteString("uuid '" + hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:] + "'")
	case Array:
		out.WriteByte('[')
		for position, item := range held.Items {
			if _, absent := item.(None); absent {
				return &NotAnEventError{Reason: "an array cannot hold an absence"}
			}
			if position > 0 {
				out.WriteString(", ")
			}
			if err := eventLiteral(item, out); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case Object:
		names := make([]string, 0, len(held.Fields))
		for name, field := range held.Fields {
			// A field holding none is left out, which is what absence means.
			if _, absent := field.(None); !absent {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			out.WriteString("{}")
			return nil
		}
		out.WriteString("{ ")
		for position, name := range names {
			if position > 0 {
				out.WriteString(", ")
			}
			eventQuoted(name, out)
			out.WriteString(": ")
			if err := eventLiteral(held.Fields[name], out); err != nil {
				return err
			}
		}
		out.WriteString(" }")
	default:
		return &NotAnEventError{Reason: "an event carries null, booleans, numbers, strings, datetimes, uuids, arrays and objects"}
	}
	return nil
}

func eventQuoted(text string, out *strings.Builder) {
	out.WriteByte('\'')
	for _, character := range text {
		if character == '\\' || character == '\'' {
			out.WriteByte('\\')
		}
		out.WriteRune(character)
	}
	out.WriteByte('\'')
}

func eventDecimal(held Decimal) string {
	mantissa := held.Mantissa
	if mantissa == nil {
		mantissa = new(big.Int)
	}
	sign := ""
	if mantissa.Sign() < 0 {
		sign = "-"
	}
	digits := new(big.Int).Abs(mantissa).String()
	scale := int(held.Scale)
	if scale == 0 {
		return "dec " + sign + digits
	}
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	return "dec " + sign + digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
}
