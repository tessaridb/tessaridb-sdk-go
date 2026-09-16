package tessaridb

import (
	"sort"
	"strings"
)

// What the four statements share.
//
// A builder offers SELECT, CREATE, UPDATE and DELETE over a single collection,
// and nothing else: anything further the caller writes as a script and sends as
// one, which is always available. A builder does not grow a clause the shared
// contract has not grown first — a clause one language has and another does not
// is exactly the divergence that contract exists to prevent.

// A refusal is captured where it happens and surfaces at Render. The FIRST one
// wins, because it is the one the caller can act on — a later refusal is often a
// consequence of the first.
type sticky struct{ err error }

func (s *sticky) fail(err error) {
	if s.err == nil && err != nil {
		s.err = err
	}
}

// Fields carried by CREATE and UPDATE.
type writeFields struct {
	sticky
	set map[string]Value
}

func (w *writeFields) put(name string, value Value) {
	if err := checkName(positionField, name); err != nil {
		w.fail(err)
		return
	}
	if w.set == nil {
		w.set = make(map[string]Value)
	}
	w.set[name] = value
}

// Fields render in ascending order of their names, compared byte by byte as
// UTF-8 — not in the order the caller set them. Two builders given the same
// fields in different orders must produce the same text and the same parameter
// numbering, which is what lets the corpus carry a set as an unordered object.
//
// Go compares strings bytewise over their UTF-8, so sort.Strings IS that rule
// rather than an approximation of it.
func sortedNames(set map[string]Value) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// `{ body: $p1, weight: $p2 }` — note the spaces immediately inside the braces.
func renderObject(b *binder, out *strings.Builder, set map[string]Value) {
	out.WriteString("{ ")
	for i, name := range sortedNames(set) {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(name)
		out.WriteString(": $")
		out.WriteString(b.bind(set[name]))
	}
	out.WriteString(" }")
}
