package tessaridb

import "strings"

// CREATE, UPDATE and DELETE, each over one record.
//
// The identity travels as a parameter and never as text, so an identity that
// happens to spell a statement is a record with an unusual name. It binds FIRST,
// before any field, which is what fixes the parameter numbering.

// CreateRecordStatement is built by CreateRecord.
type CreateRecordStatement struct {
	writeFields
	table string
	id    Value
}

// CreateRecord writes a record under an identity the caller supplies. The
// identity travels as a parameter and never as text, so an identity that happens
// to spell a statement is a record with an unusual name.
func CreateRecord(table string, id Value) *CreateRecordStatement {
	s := &CreateRecordStatement{table: table, id: id}
	s.fail(checkName(positionTable, table))
	return s
}

func (s *CreateRecordStatement) Set(name string, value Value) *CreateRecordStatement {
	s.put(name, value)
	return s
}

func (s *CreateRecordStatement) Render() (Rendered, error) {
	if s.err != nil {
		return Rendered{}, s.err
	}
	if len(s.set) == 0 {
		return Rendered{}, &BuilderError{Reason: Incomplete}
	}
	b := newBinder()
	var out strings.Builder
	out.WriteString("CREATE ")
	out.WriteString(s.table)
	// The identity binds first, before any field.
	out.WriteString(":$")
	out.WriteString(b.bind(s.id))
	out.WriteString(" = ")
	renderObject(b, &out, s.set)
	out.WriteByte(';')
	return Rendered{Script: out.String(), Parameters: b.parameters}, nil
}

// CreateInTableStatement is built by CreateInTable.
type CreateInTableStatement struct {
	writeFields
	table string
}

// CreateInTable writes a record whose identity the store allocates.
func CreateInTable(table string) *CreateInTableStatement {
	s := &CreateInTableStatement{table: table}
	s.fail(checkName(positionTable, table))
	return s
}

func (s *CreateInTableStatement) Set(name string, value Value) *CreateInTableStatement {
	s.put(name, value)
	return s
}

func (s *CreateInTableStatement) Render() (Rendered, error) {
	if s.err != nil {
		return Rendered{}, s.err
	}
	if len(s.set) == 0 {
		return Rendered{}, &BuilderError{Reason: Incomplete}
	}
	b := newBinder()
	var out strings.Builder
	out.WriteString("CREATE ")
	out.WriteString(s.table)
	out.WriteString(" = ")
	renderObject(b, &out, s.set)
	out.WriteByte(';')
	return Rendered{Script: out.String(), Parameters: b.parameters}, nil
}

// UpdateRecordStatement is built by UpdateRecord.
type UpdateRecordStatement struct {
	writeFields
	table string
	id    Value
}

// UpdateRecord changes the named fields only. A caller who wants the whole record
// replaced asks for CreateRecord, where the word says so.
func UpdateRecord(table string, id Value) *UpdateRecordStatement {
	s := &UpdateRecordStatement{table: table, id: id}
	s.fail(checkName(positionTable, table))
	return s
}

func (s *UpdateRecordStatement) Set(name string, value Value) *UpdateRecordStatement {
	s.put(name, value)
	return s
}

func (s *UpdateRecordStatement) Render() (Rendered, error) {
	if s.err != nil {
		return Rendered{}, s.err
	}
	if len(s.set) == 0 {
		return Rendered{}, &BuilderError{Reason: Incomplete}
	}
	b := newBinder()
	var out strings.Builder
	out.WriteString("UPDATE ")
	out.WriteString(s.table)
	out.WriteString(":$")
	out.WriteString(b.bind(s.id))
	out.WriteString(" SET ")
	for i, name := range sortedNames(s.set) {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(name)
		out.WriteString(" = $")
		out.WriteString(b.bind(s.set[name]))
	}
	out.WriteByte(';')
	return Rendered{Script: out.String(), Parameters: b.parameters}, nil
}

// DeleteRecordStatement is built by DeleteRecord.
type DeleteRecordStatement struct {
	sticky
	table string
	id    Value
}

// DeleteRecord removes one record by identity.
func DeleteRecord(table string, id Value) *DeleteRecordStatement {
	s := &DeleteRecordStatement{table: table, id: id}
	s.fail(checkName(positionTable, table))
	return s
}

func (s *DeleteRecordStatement) Render() (Rendered, error) {
	if s.err != nil {
		return Rendered{}, s.err
	}
	b := newBinder()
	var out strings.Builder
	out.WriteString("DELETE ")
	out.WriteString(s.table)
	out.WriteString(":$")
	out.WriteString(b.bind(s.id))
	out.WriteByte(';')
	return Rendered{Script: out.String(), Parameters: b.parameters}, nil
}
