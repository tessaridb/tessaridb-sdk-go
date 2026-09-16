package tessaridb

import (
	"fmt"
	"strings"
)

// A WHERE clause.
//
// A conjunction and a disjunction are FULLY parenthesised; a bare comparison is
// not. The parentheses are not an aid to reading — a builder does not depend on
// the node's parser and therefore does not get to assume how AND and OR
// associate. Writing them all makes the tree the caller built the tree that runs.

// Operator is one of the six comparisons the contract names.
type Operator int

const (
	Eq Operator = iota
	Ne
	Lt
	Le
	Gt
	Ge
)

func (o Operator) symbol() (string, error) {
	switch o {
	case Eq:
		return "=", nil
	case Ne:
		return "!=", nil
	case Lt:
		return "<", nil
	case Le:
		return "<=", nil
	case Gt:
		return ">", nil
	case Ge:
		return ">=", nil
	}
	// Reachable: Operator is an integer type, so Operator(42) compiles. Refusing
	// is the only safe answer — rendering an unknown operator as empty text would
	// produce a statement that parses as something else.
	return "", fmt.Errorf("tessaridb: %d is not a comparison operator", int(o))
}

// Filter is a where-clause tree. The interface is sealed — only Compare, And and
// Or implement it — so a filter that reaches a render is one of the three shapes
// the contract describes.
type Filter interface {
	render(b *binder, out *strings.Builder) error
}

type comparison struct {
	field string
	op    Operator
	value Value
}

// Compare is `<field> <op> $p`. The value becomes a parameter; a string that
// spells a statement is compared against as a string that spells a statement.
func Compare(field string, op Operator, value Value) Filter {
	return comparison{field: field, op: op, value: value}
}

func (c comparison) render(b *binder, out *strings.Builder) error {
	if err := checkName(positionField, c.field); err != nil {
		return err
	}
	symbol, err := c.op.symbol()
	if err != nil {
		return err
	}
	out.WriteString(c.field)
	out.WriteByte(' ')
	out.WriteString(symbol)
	out.WriteString(" $")
	out.WriteString(b.bind(c.value))
	return nil
}

type junction struct {
	word        string
	left, right Filter
}

// And is `(<left> AND <right>)`, parentheses included.
func And(left, right Filter) Filter { return junction{word: " AND ", left: left, right: right} }

// Or is `(<left> OR <right>)`, parentheses included.
func Or(left, right Filter) Filter { return junction{word: " OR ", left: left, right: right} }

func (j junction) render(b *binder, out *strings.Builder) error {
	out.WriteByte('(')
	if err := j.left.render(b, out); err != nil {
		return err
	}
	out.WriteString(j.word)
	if err := j.right.render(b, out); err != nil {
		return err
	}
	out.WriteByte(')')
	return nil
}
