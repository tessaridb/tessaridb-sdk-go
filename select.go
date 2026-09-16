package tessaridb

import (
	"fmt"
	"strings"
)

// SELECT, rendered on one line with each present clause preceded by one space.

// Direction is written out on every ordering, including when it is the node's
// default, so the statement says what it does.
type Direction int

const (
	Ascending Direction = iota
	Descending
)

func (d Direction) word() (string, error) {
	switch d {
	case Ascending:
		return "ASC", nil
	case Descending:
		return "DESC", nil
	}
	return "", fmt.Errorf("tessaridb: %d is not a sort direction", int(d))
}

type projection struct {
	field        string
	window       bool
	start, count uint64
}

type ordering struct {
	field     string
	direction Direction
}

// SelectStatement is built by Select.
type SelectStatement struct {
	sticky
	table   string
	items   []projection
	filter  Filter
	order   []ordering
	start   uint64
	limit   uint64
	hasFrom bool // START given
	hasCap  bool // LIMIT given
}

// Select reads from one collection. Every clause is optional.
func Select(table string) *SelectStatement {
	s := &SelectStatement{table: table}
	s.fail(checkName(positionTable, table))
	return s
}

// Field names one field. Named fields render in the order they were named,
// unlike an object body, which sorts.
func (s *SelectStatement) Field(name string) *SelectStatement {
	s.fail(checkName(positionField, name))
	s.items = append(s.items, projection{field: name})
	return s
}

// Lines reads a window of a long text field — count lines from a zero-based
// start — so a large body does not come back whole. The field arrives under its
// own name, so a caller's mapping does not change with the window.
//
// The two counts are literals rather than parameters: they are part of the
// statement's shape rather than data, and uint64 leaves nothing to smuggle
// syntax through.
func (s *SelectStatement) Lines(field string, start, count uint64) *SelectStatement {
	s.fail(checkName(positionField, field))
	s.items = append(s.items, projection{field: field, window: true, start: start, count: count})
	return s
}

// Where REPLACES any filter already set rather than combining with it. Silently
// ANDing two would make a duplicated call look as though it had worked.
func (s *SelectStatement) Where(filter Filter) *SelectStatement {
	s.filter = filter
	return s
}

func (s *SelectStatement) OrderBy(field string, direction Direction) *SelectStatement {
	s.fail(checkName(positionField, field))
	s.order = append(s.order, ordering{field: field, direction: direction})
	return s
}

func (s *SelectStatement) Start(n uint64) *SelectStatement {
	s.start, s.hasFrom = n, true
	return s
}

func (s *SelectStatement) Limit(n uint64) *SelectStatement {
	s.limit, s.hasCap = n, true
	return s
}

func (s *SelectStatement) Render() (Rendered, error) {
	if s.err != nil {
		return Rendered{}, s.err
	}
	b := newBinder()
	var out strings.Builder

	out.WriteString("SELECT ")
	if len(s.items) == 0 {
		out.WriteByte('*')
	}
	for i, item := range s.items {
		if i > 0 {
			out.WriteString(", ")
		}
		if item.window {
			fmt.Fprintf(&out, "string::lines(%s, %d, %d) AS %s",
				item.field, item.start, item.count, item.field)
			continue
		}
		out.WriteString(item.field)
	}
	out.WriteString(" FROM ")
	out.WriteString(s.table)

	if s.filter != nil {
		out.WriteString(" WHERE ")
		if err := s.filter.render(b, &out); err != nil {
			return Rendered{}, err
		}
	}
	if len(s.order) > 0 {
		out.WriteString(" ORDER BY ")
		for i, o := range s.order {
			if i > 0 {
				out.WriteString(", ")
			}
			word, err := o.direction.word()
			if err != nil {
				return Rendered{}, err
			}
			out.WriteString(o.field)
			out.WriteByte(' ')
			out.WriteString(word)
		}
	}
	// START precedes LIMIT when both are present.
	if s.hasFrom {
		fmt.Fprintf(&out, " START %d", s.start)
	}
	if s.hasCap {
		fmt.Fprintf(&out, " LIMIT %d", s.limit)
	}
	out.WriteByte(';')
	return Rendered{Script: out.String(), Parameters: b.parameters}, nil
}
