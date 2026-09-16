package tessaridb

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// Reading a table or a record reference off the HTTP surface.
//
// Both are lossy by construction and both say so in their own spelling: a
// reference the node could not resolve is written in a bracketed form, and an
// identity is written in the identity syntax rather than the value syntax.

// The brackets are what make an unresolvable reference visibly not a name: '<'
// cannot begin a table name, so a client detects the form rather than mistaking
// it for a table called "<record 7".
func jsonTableID(source string, names Names) (uint32, error) {
	if inner, ok := bracketed(source, "table "); ok {
		return jsonParseTableID(inner)
	}
	id, known := names[source]
	if !known {
		return 0, fmt.Errorf("tessaridb: %q names a table the answer's names block does not carry", source)
	}
	return id, nil
}

func bracketed(source, prefix string) (string, bool) {
	if !strings.HasPrefix(source, "<"+prefix) || !strings.HasSuffix(source, ">") {
		return "", false
	}
	return source[len("<")+len(prefix) : len(source)-1], true
}

func jsonParseTableID(source string) (uint32, error) {
	id, err := strconv.ParseUint(source, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("tessaridb: %q is not a table id", source)
	}
	return uint32(id), nil
}

func jsonRecord(source string, of IDKind, names Names) (Value, error) {
	if inner, ok := bracketed(source, "record "); ok {
		table, identity, found := strings.Cut(inner, ":")
		if !found {
			return nil, fmt.Errorf("tessaridb: %q is not a record", source)
		}
		id, err := jsonParseTableID(table)
		if err != nil {
			return nil, err
		}
		value, err := jsonRecordID(identity, of)
		return Record{Table: id, ID: value}, err
	}
	// A table name cannot contain a colon, so the FIRST one separates. An
	// identity may contain as many as it likes and they are part of the name —
	// users:a:b cannot be split any other way.
	table, identity, found := strings.Cut(source, ":")
	if !found {
		return nil, fmt.Errorf("tessaridb: %q is not a record", source)
	}
	id, err := jsonTableID(table, names)
	if err != nil {
		return nil, err
	}
	value, err := jsonRecordID(identity, of)
	return Record{Table: id, ID: value}, err
}

// The id half is written in the identity syntax rather than the value syntax: a
// uuid loses its hyphens and bytes gain a 0x. The integer 1 and the text '1' are
// written identically, which is why the kind is supplied and never sniffed.
func jsonRecordID(source string, of IDKind) (RecordID, error) {
	switch of {
	case IDInteger:
		value, err := strconv.ParseInt(source, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("tessaridb: %q is not an integer record id", source)
		}
		return IntegerID{Value: value}, nil
	case IDText:
		return TextID{Value: source}, nil
	case IDUUID:
		value, err := jsonUUIDBytes(source)
		return UUIDID{Value: value}, err
	case IDBytes:
		rest, ok := strings.CutPrefix(source, "0x")
		if !ok {
			return nil, fmt.Errorf("tessaridb: a bytes record id is written with a 0x prefix")
		}
		value, err := hex.DecodeString(rest)
		if err != nil {
			return nil, fmt.Errorf("tessaridb: %q is not hex", rest)
		}
		return BytesID{Value: value}, nil
	}
	return nil, fmt.Errorf("tessaridb: %d is not a record id kind", int(of))
}
