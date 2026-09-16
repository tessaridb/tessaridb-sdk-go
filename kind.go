package tessaridb

// What the catalog says a field holds.
//
// §5.7 is a decision rather than a translation: JSON has six types and this store
// has seventeen, so a value's type is NOT recoverable from the JSON alone for
// most of the table. The specification says what a caller does about it — read
// the kind from the field's declaration in the catalog, or use the wire
// protocol, where every value carries its tag.
//
// This is the first of those. Give the reader the JSON and the kind, and it
// returns the value the store held. Without a kind there is nothing honest to
// do: "12.34" is a decimal or a string, "1h30m" is a duration or a string, and a
// reader that guessed would be right most of the time, which is worse than being
// wrong all of it.
//
// One spelling stays lossy even with the kind supplied, and it is named rather
// than papered over: a float -0.0 is written 0 (§5.7.1 — positional, no trailing
// ".0", and the value is normalised before it is written), so no reader can tell
// it from +0.0.

// A Kind is sealed, like Value and Shape, so a kind that reaches the reader is
// one this package knows.
type Kind interface{ isKind() }

type NullKind struct{}
type BoolKind struct{}
type IntegerKind struct{}
type DecimalKind struct{}
type FloatKind struct{}
type StringKind struct{}
type BytesKind struct{}
type DurationKind struct{}
type DatetimeKind struct{}
type UUIDKind struct{}
type TableKind struct{}
type RegexKind struct{}
type GeometryKind struct{}

// RecordKind carries the identity's kind, because the JSON cannot. "users:7" is
// the integer 7 and the text '7' written identically (§5.7.1), and those are the
// two cases the corpus uses to prove a reader honours what it was told rather
// than sniffing the string.
type RecordKind struct{ ID IDKind }

// ArrayKind and SetKind take either one kind for every element, or one per
// position — a TessariQL array need not be uniform. Each wins when it is set.
type ArrayKind struct {
	Of   Kind
	Each []Kind
}

type SetKind struct {
	Of   Kind
	Each []Kind
}

// ObjectKind declares the fields. Every declared field is read, INCLUDING the
// ones the answer omitted: an omitted field is none, and dropping it would lose
// exactly the distinction this transport carries by absence.
type ObjectKind struct{ Fields map[string]Kind }

type RangeKind struct{ Of Kind }

func (NullKind) isKind()     {}
func (BoolKind) isKind()     {}
func (IntegerKind) isKind()  {}
func (DecimalKind) isKind()  {}
func (FloatKind) isKind()    {}
func (StringKind) isKind()   {}
func (BytesKind) isKind()    {}
func (DurationKind) isKind() {}
func (DatetimeKind) isKind() {}
func (UUIDKind) isKind()     {}
func (TableKind) isKind()    {}
func (RegexKind) isKind()    {}
func (GeometryKind) isKind() {}
func (RecordKind) isKind()   {}
func (ArrayKind) isKind()    {}
func (SetKind) isKind()      {}
func (ObjectKind) isKind()   {}
func (RangeKind) isKind()    {}

// IDKind names which of the four record identities a field holds.
type IDKind int

const (
	IDInteger IDKind = iota
	IDText
	IDUUID
	IDBytes
)

// Names is the answer's names block (§3.9) read the other way round: a table
// name to its id. A reference whose name is not in it is unresolvable, and the
// node writes those in the bracketed form rather than guessing.
type Names map[string]uint32

// Absent is what a caller passes for a key that was not in the object. It is the
// language's none, and it is a distinct type rather than a nil because JSON null
// is a different value that is also spelled with nothing in Go.
type Absent struct{}
