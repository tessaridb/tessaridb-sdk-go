package tessaridb

import (
	"strconv"
	"strings"
)

// Building a statement.
//
// The rendering is fixed by a shared contract rather than by this package, so the
// same query built in any client language produces the same text and the same
// parameter numbering. What that contract guarantees above everything else is
// that a caller's value never reaches the statement text: every value becomes a
// bound parameter, and the text carries only the reference.
//
// Names are the other half of it, and they are not values. A table or field name
// is grammar — a parameter cannot supply one — so it is written into the text
// directly, and that is safe only because the check stands in front of the
// interpolation rather than somewhere else.

// RefusalReason is why a builder would not render. They are the shared
// contract's own vocabulary, and a builder never invents one of its own.
type RefusalReason string

const (
	// NotAName: a grammatical position was given a string that is not a name.
	NotAName RefusalReason = "not-a-name"
	// Incomplete: a statement that cannot be rendered at all, such as a CREATE
	// with no fields.
	Incomplete RefusalReason = "incomplete"
	// NotASpan: a STALENESS bound that is not digits followed by one of the
	// node's eight units.
	NotASpan RefusalReason = "not-a-span"
	// NotAnAnswerer: an ANSWERED BY that is neither ANY nor LEADER.
	NotAnAnswerer RefusalReason = "not-an-answerer"
)

// The grammatical positions a name can occupy, as the contract words them.
const (
	positionTable = "a table"
	positionField = "a field"
)

// A BuilderError is a refusal, returned to the caller rather than sent to the
// node. The caller is here now and the node is not.
type BuilderError struct {
	Reason RefusalReason
	// What names the position — "a table" or "a field". Empty when the reason is
	// Incomplete, which is about the statement rather than about a name.
	What string
	Name string
}

func (e *BuilderError) Error() string {
	switch e.Reason {
	case Incomplete:
		return "tessaridb: a statement with no fields cannot be rendered"
	case NotASpan:
		return "tessaridb: " + strconv.Quote(e.Name) +
			" is not a span — write digits and one of ns, us, ms, s, m, h, d, w, as in \"30s\""
	case NotAnAnswerer:
		return "tessaridb: " + strconv.Quote(e.Name) + " is not an answerer — write ANY or LEADER"
	}
	return "tessaridb: " + strconv.Quote(e.Name) + " is not a name, in " + e.What
}

// spanUnits are the node's own eight, longest first so "ms" is read before "m".
var spanUnits = []string{"ms", "ns", "us", "s", "m", "h", "d", "w"}

// checkSpan holds the contract's span production: 1*( 1*DIGIT unit ).
//
// A span is written into the statement TEXT rather than bound, because a node
// refuses a parameter in that position. This check is therefore what keeps the
// guarantee that a caller's characters never reach a script unexamined.
//
// The VALUE is never judged here. A bound tighter than the cluster's floor is
// the node's refusal to make and its message names the floor; a client that
// guessed would be wrong on the next cluster.
func checkSpan(text string) error {
	rest, seen := text, false
	for len(rest) > 0 {
		digits := 0
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits == 0 {
			return &BuilderError{Reason: NotASpan, Name: text}
		}
		rest = rest[digits:]
		matched := ""
		for _, unit := range spanUnits {
			if strings.HasPrefix(rest, unit) {
				matched = unit
				break
			}
		}
		if matched == "" {
			return &BuilderError{Reason: NotASpan, Name: text}
		}
		rest = rest[len(matched):]
		seen = true
	}
	if !seen {
		return &BuilderError{Reason: NotASpan, Name: text}
	}
	return nil
}

// checkAnswerer holds ANY or LEADER, and no third. The direction a guess fails
// in is the unsafe one: somebody writing MASTER means the leader, and passing an
// unrecognised word through would have the read answered by whatever copy came
// first.
func checkAnswerer(word string) error {
	if word != "ANY" && word != "LEADER" {
		return &BuilderError{Reason: NotAnAnswerer, Name: word}
	}
	return nil
}

// checkName holds the contract's production: a letter or underscore, then
// letters, digits and underscores, ASCII throughout.
//
// It is deliberately NARROWER than the node's lexer. A guard that reasons about
// what the lexer would accept has to be re-checked every time the lexer changes;
// this one does not, and the cost is that a caller with an exotic but legal name
// writes the statement themselves.
//
// Scanning bytes rather than runes is correct precisely because every accepted
// byte is ASCII: a multi-byte rune's first byte is >= 0x80 and is refused there.
//
// A string that is not a name is refused and never quoted into acceptance —
// quoting would turn the caller's mistake into a statement that runs and means
// something else.
func checkName(what, name string) error {
	refuse := &BuilderError{Reason: NotAName, What: what, Name: name}
	if name == "" {
		return refuse
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_', 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z':
		case i > 0 && '0' <= c && c <= '9':
		default:
			return refuse
		}
	}
	return nil
}

// Rendered is a statement and the values that travel beside it. It is exactly
// what Conn.Execute takes.
type Rendered struct {
	Script     string
	Parameters map[string]Value
}

// The binder owns the one guarantee. Parameters are named in binding order
// counting from zero, and binding happens DURING rendering, left to right — so
// the numbering matches the text a reader meets by construction, rather than by a
// second traversal that could drift from the first.
type binder struct{ parameters map[string]Value }

func newBinder() *binder { return &binder{parameters: make(map[string]Value)} }

// bind returns the reference name, without the leading '$' the text carries.
func (b *binder) bind(value Value) string {
	reference := "p" + strconv.Itoa(len(b.parameters))
	b.parameters[reference] = value
	return reference
}
