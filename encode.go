package tessaridb

import "sort"

// Encode writes one value in the protocol's value encoding.
func Encode(v Value) ([]byte, error) {
	w := &writer{}
	if err := writeValue(w, v); err != nil {
		return nil, err
	}
	return w.buf, nil
}

func writeValue(w *writer, v Value) error {
	switch v := v.(type) {
	case None:
		w.u8(tagNone)
	case Null:
		w.u8(tagNull)
	case Bool:
		w.u8(tagBool)
		if v.Value {
			w.u8(1)
		} else {
			w.u8(0)
		}
	case Integer:
		w.u8(tagNumber)
		w.u8(numberInteger)
		w.i64Inverted(v.Value)
	case Float:
		w.u8(tagNumber)
		w.u8(numberFloat)
		w.f64Bits(v.Value)
	case Decimal:
		w.u8(tagNumber)
		w.u8(numberDecimal)
		if v.Mantissa == nil {
			return protocolf("a decimal needs a mantissa")
		}
		w.i128(v.Mantissa)
		w.u32(v.Scale)
	case Text:
		w.u8(tagText)
		w.text(v.Value)
	case Bytes:
		w.u8(tagBytes)
		w.lenbytes(v.Value)
	case Duration:
		w.u8(tagDuration)
		return writeSecondsAndNanos(w, v.Seconds, v.Nanos, "duration")
	case Datetime:
		w.u8(tagDatetime)
		return writeSecondsAndNanos(w, v.Seconds, v.Nanos, "datetime")
	case UUID:
		w.u8(tagUUID)
		w.fixed(v.Value[:])
	case Table:
		w.u8(tagTable)
		w.u32(v.ID)
	case Record:
		w.u8(tagRecord)
		w.u32(v.Table)
		return writeRecordID(w, v.ID)
	case Array:
		w.u8(tagArray)
		return writeItems(w, v.Items)
	case Set:
		w.u8(tagSet)
		return writeItems(w, v.Items)
	case Object:
		w.u8(tagObject)
		w.u32(uint32(len(v.Fields)))
		// Name order is a client-side convenience rather than a protocol
		// requirement — the node re-normalises on decode. Emitting it sorted is
		// what makes two equal values encode to equal bytes, which is what lets
		// a caller compare or cache encodings of its own.
		names := make([]string, 0, len(v.Fields))
		for name := range v.Fields {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			w.text(name)
			if err := writeValue(w, v.Fields[name]); err != nil {
				return err
			}
		}
	case Range:
		w.u8(tagRange)
		if err := writeBound(w, v.Start); err != nil {
			return err
		}
		return writeBound(w, v.End)
	case Geometry:
		w.u8(tagGeometry)
		return writeShape(w, v.Shape)
	case Regex:
		w.u8(tagRegex)
		w.text(v.Pattern)
	default:
		return protocolf("a value this build cannot write: %T", v)
	}
	return nil
}

func writeItems(w *writer, items []Value) error {
	w.u32(uint32(len(items)))
	for _, item := range items {
		if err := writeValue(w, item); err != nil {
			return err
		}
	}
	return nil
}

func writeSecondsAndNanos(w *writer, seconds int64, nanos uint32, what string) error {
	// Nanoseconds outside the sub-second range are an error rather than a wrap:
	// carrying 1_500_000_000 here would mean a different instant to every other
	// implementation, and nothing would report it.
	if nanos >= nanosCeiling {
		return protocolf("%s nanoseconds %d is outside 0..%d", what, nanos, nanosCeiling-1)
	}
	w.i64Inverted(seconds)
	w.u32(nanos)
	return nil
}

func writeRecordID(w *writer, id RecordID) error {
	switch id := id.(type) {
	case IntegerID:
		w.u8(idInteger)
		w.i64Inverted(id.Value)
	case TextID:
		w.u8(idText)
		w.varbytes([]byte(id.Value))
	case UUIDID:
		w.u8(idUUID)
		w.fixed(id.Value[:])
	case BytesID:
		w.u8(idBytes)
		w.varbytes(id.Value)
	default:
		return protocolf("a record id this build cannot write: %T", id)
	}
	return nil
}

func writeBound(w *writer, b Bound) error {
	switch b := b.(type) {
	case Unbounded:
		w.u8(boundUnbounded)
	case Included:
		w.u8(boundIncluded)
		return writeValue(w, b.Value)
	case Excluded:
		w.u8(boundExcluded)
		return writeValue(w, b.Value)
	default:
		return protocolf("a bound this build cannot write: %T", b)
	}
	return nil
}

func writePosition(w *writer, p Position) {
	// Longitude first. RFC 7946 §3.1.1 fixes it and the coordinates are bits
	// rather than text, so 0.0 and -0.0 are different values on the wire.
	w.f64Bits(p.Lon)
	w.f64Bits(p.Lat)
}

func writePositions(w *writer, ps []Position) {
	w.u32(uint32(len(ps)))
	for _, p := range ps {
		writePosition(w, p)
	}
}

func writePolygon(w *writer, p Polygon) {
	writePositions(w, p.Exterior)
	w.u32(uint32(len(p.Interiors)))
	for _, ring := range p.Interiors {
		writePositions(w, ring)
	}
}

func writeShape(w *writer, s Shape) error {
	switch s := s.(type) {
	case Point:
		w.u8(shapePoint)
		writePosition(w, s.Position)
	case Line:
		w.u8(shapeLine)
		writePositions(w, s.Positions)
	case PolygonShape:
		w.u8(shapePolygon)
		writePolygon(w, s.Polygon)
	case MultiPoint:
		w.u8(shapeMultiPoint)
		writePositions(w, s.Positions)
	case MultiLine:
		w.u8(shapeMultiLine)
		w.u32(uint32(len(s.Lines)))
		for _, line := range s.Lines {
			writePositions(w, line)
		}
	case MultiPolygon:
		w.u8(shapeMultiPolygon)
		w.u32(uint32(len(s.Polygons)))
		for _, p := range s.Polygons {
			writePolygon(w, p)
		}
	case Collection:
		w.u8(shapeCollection)
		w.u32(uint32(len(s.Geometries)))
		for _, g := range s.Geometries {
			if err := writeShape(w, g); err != nil {
				return err
			}
		}
	default:
		return protocolf("a geometry this build cannot write: %T", s)
	}
	return nil
}
