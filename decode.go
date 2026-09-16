package tessaridb

import "math"

// Decode reads one value and requires the buffer to be exhausted.
//
// A value payload carries no length of its own, so there is no way to resume
// after one: trailing bytes mean the reader and the writer disagree about the
// shape, and continuing would read the next value from the middle of this one.
func Decode(b []byte) (Value, error) {
	r := &reader{buf: b}
	v, err := readValue(r)
	if err != nil {
		return nil, err
	}
	if r.remaining() != 0 {
		return nil, protocolf("%d bytes remain after a complete value", r.remaining())
	}
	return v, nil
}

func readValue(r *reader) (Value, error) {
	tag, err := r.u8("a value tag")
	if err != nil {
		return nil, err
	}
	switch tag {
	case tagNone:
		return None{}, nil
	case tagNull:
		return Null{}, nil
	case tagBool:
		b, err := r.u8("a bool")
		if err != nil {
			return nil, err
		}
		return Bool{Value: b != 0}, nil
	case tagNumber:
		return readNumber(r)
	case tagText:
		s, err := r.text("a string")
		return Text{Value: s}, err
	case tagBytes:
		b, err := r.lenbytes("bytes")
		return Bytes{Value: b}, err
	case tagDuration:
		s, n, err := readSecondsAndNanos(r, "duration")
		return Duration{Seconds: s, Nanos: n}, err
	case tagDatetime:
		s, n, err := readSecondsAndNanos(r, "datetime")
		return Datetime{Seconds: s, Nanos: n}, err
	case tagUUID:
		b, err := r.fixed(16, "a uuid")
		if err != nil {
			return nil, err
		}
		var u UUID
		copy(u.Value[:], b)
		return u, nil
	case tagTable:
		id, err := r.u32("a table id")
		return Table{ID: id}, err
	case tagRecord:
		table, err := r.u32("a record's table")
		if err != nil {
			return nil, err
		}
		id, err := readRecordID(r)
		return Record{Table: table, ID: id}, err
	case tagArray:
		items, err := readItems(r)
		return Array{Items: items}, err
	case tagSet:
		items, err := readItems(r)
		return Set{Items: items}, err
	case tagObject:
		count, err := r.u32("an object's field count")
		if err != nil {
			return nil, err
		}
		fields := make(map[string]Value, count)
		for i := uint32(0); i < count; i++ {
			name, err := r.text("a field name")
			if err != nil {
				return nil, err
			}
			value, err := readValue(r)
			if err != nil {
				return nil, err
			}
			fields[name] = value
		}
		return Object{Fields: fields}, nil
	case tagRange:
		start, err := readBound(r)
		if err != nil {
			return nil, err
		}
		end, err := readBound(r)
		return Range{Start: start, End: end}, err
	case tagGeometry:
		s, err := readShape(r)
		return Geometry{Shape: s}, err
	case tagRegex:
		p, err := r.text("a regex")
		return Regex{Pattern: p}, err
	default:
		// An unknown tag is an error rather than a guess: a value has no length
		// to skip over, so there is nothing to step past.
		return nil, protocolf("unknown value tag 0x%02x", tag)
	}
}

func readNumber(r *reader) (Value, error) {
	kind, err := r.u8("a number kind")
	if err != nil {
		return nil, err
	}
	switch kind {
	case numberInteger:
		n, err := r.i64Inverted("an integer")
		return Integer{Value: n}, err
	case numberFloat:
		bits, err := r.u64("a float")
		return Float{Value: math.Float64frombits(bits)}, err
	case numberDecimal:
		mantissa, err := r.i128("a decimal mantissa")
		if err != nil {
			return nil, err
		}
		scale, err := r.u32("a decimal scale")
		return Decimal{Mantissa: mantissa, Scale: scale}, err
	default:
		return nil, protocolf("unknown number kind 0x%02x", kind)
	}
}

func readSecondsAndNanos(r *reader, what string) (int64, uint32, error) {
	seconds, err := r.i64Inverted(what + " seconds")
	if err != nil {
		return 0, 0, err
	}
	nanos, err := r.u32(what + " nanoseconds")
	if err != nil {
		return 0, 0, err
	}
	if nanos >= nanosCeiling {
		return 0, 0, protocolf("%s nanoseconds %d is outside 0..%d", what, nanos, nanosCeiling-1)
	}
	return seconds, nanos, nil
}

func readItems(r *reader) ([]Value, error) {
	count, err := r.u32("an item count")
	if err != nil {
		return nil, err
	}
	items := make([]Value, 0, min(int(count), 1024))
	for i := uint32(0); i < count; i++ {
		item, err := readValue(r)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func readRecordID(r *reader) (RecordID, error) {
	tag, err := r.u8("a record id tag")
	if err != nil {
		return nil, err
	}
	switch tag {
	case idInteger:
		n, err := r.i64Inverted("an integer record id")
		return IntegerID{Value: n}, err
	case idText:
		b, err := r.varbytes("a text record id")
		return TextID{Value: string(b)}, err
	case idUUID:
		b, err := r.fixed(16, "a uuid record id")
		if err != nil {
			return nil, err
		}
		var id UUIDID
		copy(id.Value[:], b)
		return id, nil
	case idBytes:
		b, err := r.varbytes("a bytes record id")
		return BytesID{Value: b}, err
	default:
		return nil, protocolf("unknown record id tag 0x%02x", tag)
	}
}

func readBound(r *reader) (Bound, error) {
	tag, err := r.u8("a bound tag")
	if err != nil {
		return nil, err
	}
	switch tag {
	case boundUnbounded:
		return Unbounded{}, nil
	case boundIncluded:
		v, err := readValue(r)
		return Included{Value: v}, err
	case boundExcluded:
		v, err := readValue(r)
		return Excluded{Value: v}, err
	default:
		return nil, protocolf("unknown bound tag 0x%02x", tag)
	}
}

func readPosition(r *reader) (Position, error) {
	lon, err := r.u64("a longitude")
	if err != nil {
		return Position{}, err
	}
	lat, err := r.u64("a latitude")
	if err != nil {
		return Position{}, err
	}
	return Position{Lon: math.Float64frombits(lon), Lat: math.Float64frombits(lat)}, nil
}

func readPositions(r *reader) ([]Position, error) {
	count, err := r.u32("a position count")
	if err != nil {
		return nil, err
	}
	ps := make([]Position, 0, min(int(count), 1024))
	for i := uint32(0); i < count; i++ {
		p, err := readPosition(r)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, nil
}

func readPolygon(r *reader) (Polygon, error) {
	exterior, err := readPositions(r)
	if err != nil {
		return Polygon{}, err
	}
	count, err := r.u32("a hole count")
	if err != nil {
		return Polygon{}, err
	}
	interiors := make([]Ring, 0, min(int(count), 256))
	for i := uint32(0); i < count; i++ {
		ring, err := readPositions(r)
		if err != nil {
			return Polygon{}, err
		}
		interiors = append(interiors, ring)
	}
	return Polygon{Exterior: exterior, Interiors: interiors}, nil
}

// readShape does not enforce validity — a ring that does not close, a hole that
// escapes its polygon. The codec reports what the store holds; rejecting it here
// would answer a question the store already answered.
func readShape(r *reader) (Shape, error) {
	kind, err := r.u8("a geometry kind")
	if err != nil {
		return nil, err
	}
	switch kind {
	case shapePoint:
		p, err := readPosition(r)
		return Point{Position: p}, err
	case shapeLine:
		ps, err := readPositions(r)
		return Line{Positions: ps}, err
	case shapePolygon:
		p, err := readPolygon(r)
		return PolygonShape{Polygon: p}, err
	case shapeMultiPoint:
		ps, err := readPositions(r)
		return MultiPoint{Positions: ps}, err
	case shapeMultiLine:
		count, err := r.u32("a line count")
		if err != nil {
			return nil, err
		}
		lines := make([]Ring, 0, min(int(count), 1024))
		for i := uint32(0); i < count; i++ {
			ps, err := readPositions(r)
			if err != nil {
				return nil, err
			}
			lines = append(lines, ps)
		}
		return MultiLine{Lines: lines}, nil
	case shapeMultiPolygon:
		count, err := r.u32("a polygon count")
		if err != nil {
			return nil, err
		}
		polygons := make([]Polygon, 0, min(int(count), 256))
		for i := uint32(0); i < count; i++ {
			p, err := readPolygon(r)
			if err != nil {
				return nil, err
			}
			polygons = append(polygons, p)
		}
		return MultiPolygon{Polygons: polygons}, nil
	case shapeCollection:
		count, err := r.u32("a geometry count")
		if err != nil {
			return nil, err
		}
		shapes := make([]Shape, 0, min(int(count), 256))
		for i := uint32(0); i < count; i++ {
			s, err := readShape(r)
			if err != nil {
				return nil, err
			}
			shapes = append(shapes, s)
		}
		return Collection{Geometries: shapes}, nil
	default:
		return nil, protocolf("unknown geometry kind 0x%02x", kind)
	}
}
