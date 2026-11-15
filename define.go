package tessaridb

import (
	"fmt"
	"sort"
	"strings"
)

// DEFINE TABLE, the one definition the builder carries (contract §4.11):
// a name, the columns it declares, and the storage layout clause. Definitions
// name things and hold no values, so nothing binds — the partition count is
// the statement's shape, like START and LIMIT, and arrives as an integer.

// StoreKind is the layout word, §4.11.
type StoreKind int

const (
	// StoreRow is STORE ROW — the default said out loud.
	StoreRow StoreKind = iota
	// StoreColumn is STORE COLUMN — the columnar projection clause.
	StoreColumn
)

func (k StoreKind) word() string {
	switch k {
	case StoreRow:
		return "ROW"
	case StoreColumn:
		return "COLUMN"
	}
	return ""
}

// checkStoreKind holds the §4.11 production: row or column, and no third. A
// string that arrived from outside is exactly what must not reach the script
// unexamined.
func checkStoreKind(word string) (StoreKind, error) {
	switch word {
	case "row":
		return StoreRow, nil
	case "column":
		return StoreColumn, nil
	}
	return StoreRow, &BuilderError{Reason: NotAStoreKind, Name: word}
}

// DefineTableStatement is built by DefineTable.
type DefineTableStatement struct {
	sticky
	table      string
	columns    map[string]string
	store      StoreKind
	hasStore   bool
	partitions *struct {
		count uint64
		by    string
	}
}

// DefineTable defines a table: its columns, and the storage layout clause.
func DefineTable(table string) *DefineTableStatement {
	d := &DefineTableStatement{table: table, columns: map[string]string{}}
	d.fail(checkName(positionTable, table))
	return d
}

// Column declares one column: the field's name and its type word, as the
// definition spells them. Columns render in the contract's byte order (§4.8),
// so call order is not the rendered order.
func (d *DefineTableStatement) Column(name, kind string) *DefineTableStatement {
	if err := checkName(positionField, name); err != nil {
		d.fail(err)
		return d
	}
	d.columns[name] = kind
	return d
}

// Store is STORE ROW or STORE COLUMN from a caller's word — checked, because
// a string that arrived from outside is exactly what must not reach the
// script unexamined.
func (d *DefineTableStatement) Store(kind string) *DefineTableStatement {
	store, err := checkStoreKind(kind)
	if err != nil {
		d.fail(err)
		return d
	}
	d.store, d.hasStore = store, true
	return d
}

// StoreRow is STORE ROW.
func (d *DefineTableStatement) StoreRow() *DefineTableStatement {
	d.store, d.hasStore = StoreRow, true
	return d
}

// StoreColumn is STORE COLUMN.
func (d *DefineTableStatement) StoreColumn() *DefineTableStatement {
	d.store, d.hasStore = StoreColumn, true
	return d
}

// Partitions is PARTITIONS <count> BY <field>. Refused here when the count is
// not a whole number of at least one, and at render beside any store other
// than column.
func (d *DefineTableStatement) Partitions(count uint64, by string) *DefineTableStatement {
	if count < 1 {
		d.fail(&BuilderError{Reason: NotAPartitionCount, Name: fmt.Sprint(count)})
		return d
	}
	if err := checkName(positionField, by); err != nil {
		d.fail(err)
		return d
	}
	d.partitions = &struct {
		count uint64
		by    string
	}{count: count, by: by}
	return d
}

// Render renders the statement. A definition binds nothing: every position
// names a thing, and the count is the statement's shape.
func (d *DefineTableStatement) Render() (Rendered, error) {
	if d.err != nil {
		return Rendered{}, d.err
	}
	names := make([]string, 0, len(d.columns))
	for name := range d.columns {
		names = append(names, name)
	}
	sort.Strings(names)
	columns := make([]string, 0, len(names))
	for _, name := range names {
		columns = append(columns, name+" "+d.columns[name])
	}
	var script strings.Builder
	fmt.Fprintf(&script, "DEFINE TABLE %s (%s)", d.table, strings.Join(columns, ", "))
	if d.hasStore {
		fmt.Fprintf(&script, " STORE %s", d.store.word())
	}
	if d.partitions != nil {
		if !d.hasStore || d.store != StoreColumn {
			return Rendered{}, &BuilderError{Reason: PartitionsWithoutStore}
		}
		fmt.Fprintf(&script, " PARTITIONS %d BY %s", d.partitions.count, d.partitions.by)
	}
	script.WriteByte(';')
	return Rendered{Script: script.String()}, nil
}
