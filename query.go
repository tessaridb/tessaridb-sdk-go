package tessaridb

import "strconv"

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

// RefusalReason is why a builder would not render. There are two, they are the
// shared contract's own vocabulary, and a builder never invents a third.
type RefusalReason string

const (
	// NotAName: a grammatical position was given a string that is not a name.
	NotAName RefusalReason = "not-a-name"
	// Incomplete: a statement that cannot be rendered at all, such as a CREATE
	// with no fields.
	Incomplete RefusalReason = "incomplete"
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
	if e.Reason == Incomplete {
		return "tessaridb: a statement with no fields cannot be rendered"
	}
	return "tessaridb: " + strconv.Quote(e.Name) + " is not a name, in " + e.What
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
