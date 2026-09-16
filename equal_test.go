package tessaridb

import (
	"bytes"
	"math"
	"reflect"
)

// sameValue compares two values the way the protocol distinguishes them, which
// reflect.DeepEqual does not: it says NaN != NaN, and it says nothing useful
// about -0 versus 0 because those ARE equal to it — and the protocol keeps them
// apart. A corpus vector for each exists precisely to catch a codec that loses
// one, so the comparison has to be able to see it.
func sameValue(a, b Value) bool {
	switch a := a.(type) {
	case Float:
		bb, ok := b.(Float)
		if !ok {
			return false
		}
		// Bit equality: NaN equals NaN, and -0 does not equal 0.
		return math.Float64bits(a.Value) == math.Float64bits(bb.Value)
	case Decimal:
		bb, ok := b.(Decimal)
		return ok && a.Scale == bb.Scale && a.Mantissa.Cmp(bb.Mantissa) == 0
	case Bytes:
		bb, ok := b.(Bytes)
		return ok && bytes.Equal(a.Value, bb.Value)
	case Array:
		bb, ok := b.(Array)
		return ok && sameItems(a.Items, bb.Items)
	case Set:
		bb, ok := b.(Set)
		return ok && sameItems(a.Items, bb.Items)
	case Object:
		bb, ok := b.(Object)
		if !ok || len(a.Fields) != len(bb.Fields) {
			return false
		}
		for name, value := range a.Fields {
			other, there := bb.Fields[name]
			if !there || !sameValue(value, other) {
				return false
			}
		}
		return true
	case Range:
		bb, ok := b.(Range)
		return ok && sameBound(a.Start, bb.Start) && sameBound(a.End, bb.End)
	case Geometry:
		bb, ok := b.(Geometry)
		return ok && sameShape(a.Shape, bb.Shape)
	default:
		return reflect.DeepEqual(a, b)
	}
}

func sameItems(a, b []Value) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameValue(a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameBound(a, b Bound) bool {
	switch a := a.(type) {
	case Unbounded:
		_, ok := b.(Unbounded)
		return ok
	case Included:
		bb, ok := b.(Included)
		return ok && sameValue(a.Value, bb.Value)
	case Excluded:
		bb, ok := b.(Excluded)
		return ok && sameValue(a.Value, bb.Value)
	}
	return false
}

func samePositions(a, b []Position) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		// Bits again: 0.0 and -0.0 are different coordinates on this wire.
		if math.Float64bits(a[i].Lon) != math.Float64bits(b[i].Lon) ||
			math.Float64bits(a[i].Lat) != math.Float64bits(b[i].Lat) {
			return false
		}
	}
	return true
}

func samePolygon(a, b Polygon) bool {
	if !samePositions(a.Exterior, b.Exterior) || len(a.Interiors) != len(b.Interiors) {
		return false
	}
	for i := range a.Interiors {
		if !samePositions(a.Interiors[i], b.Interiors[i]) {
			return false
		}
	}
	return true
}

func sameShape(a, b Shape) bool {
	switch a := a.(type) {
	case Point:
		bb, ok := b.(Point)
		return ok && samePositions([]Position{a.Position}, []Position{bb.Position})
	case Line:
		bb, ok := b.(Line)
		return ok && samePositions(a.Positions, bb.Positions)
	case PolygonShape:
		bb, ok := b.(PolygonShape)
		return ok && samePolygon(a.Polygon, bb.Polygon)
	case MultiPoint:
		bb, ok := b.(MultiPoint)
		return ok && samePositions(a.Positions, bb.Positions)
	case MultiLine:
		bb, ok := b.(MultiLine)
		if !ok || len(a.Lines) != len(bb.Lines) {
			return false
		}
		for i := range a.Lines {
			if !samePositions(a.Lines[i], bb.Lines[i]) {
				return false
			}
		}
		return true
	case MultiPolygon:
		bb, ok := b.(MultiPolygon)
		if !ok || len(a.Polygons) != len(bb.Polygons) {
			return false
		}
		for i := range a.Polygons {
			if !samePolygon(a.Polygons[i], bb.Polygons[i]) {
				return false
			}
		}
		return true
	case Collection:
		bb, ok := b.(Collection)
		if !ok || len(a.Geometries) != len(bb.Geometries) {
			return false
		}
		for i := range a.Geometries {
			if !sameShape(a.Geometries[i], bb.Geometries[i]) {
				return false
			}
		}
		return true
	}
	return false
}
