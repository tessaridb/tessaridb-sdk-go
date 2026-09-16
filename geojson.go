package tessaridb

import "fmt"

// GeoJSON, RFC 7946.
//
// Coordinates are [longitude, latitude]. The opposite order is the most common
// bug in geospatial code precisely because it is silent — a point in Paris
// becomes a point in the Indian Ocean, which is a perfectly valid place.
//
// A non-finite coordinate is written null (§5.7). It cannot arise from a
// well-formed shape and can arise from bytes, so the surface reports what it
// holds rather than inventing a number — and this reader carries that through as
// a NaN rather than refusing, for the same reason.
//
// Validity is deliberately not enforced: ring closure, winding order and
// self-intersection are the store's business, and a client that refused a shape
// the store accepted would be unable to read back what it wrote.

func jsonGeometry(j any) (Shape, error) {
	object, ok := j.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tessaridb: a geometry is a JSON object, got %T", j)
	}
	kind, ok := object["type"].(string)
	if !ok {
		return nil, fmt.Errorf("tessaridb: a geometry names its type")
	}
	if kind == "GeometryCollection" {
		raw, ok := object["geometries"].([]any)
		if !ok {
			return nil, fmt.Errorf("tessaridb: a GeometryCollection carries geometries")
		}
		shapes := make([]Shape, 0, len(raw))
		for _, each := range raw {
			shape, err := jsonGeometry(each)
			if err != nil {
				return nil, err
			}
			shapes = append(shapes, shape)
		}
		return Collection{Geometries: shapes}, nil
	}

	coordinates, present := object["coordinates"]
	if !present {
		return nil, fmt.Errorf("tessaridb: a %s carries coordinates", kind)
	}
	switch kind {
	case "Point":
		position, err := jsonPosition(coordinates)
		return Point{Position: position}, err
	case "LineString":
		positions, err := jsonPositions(coordinates)
		return Line{Positions: positions}, err
	case "Polygon":
		polygon, err := jsonPolygon(coordinates)
		return PolygonShape{Polygon: polygon}, err
	case "MultiPoint":
		positions, err := jsonPositions(coordinates)
		return MultiPoint{Positions: positions}, err
	case "MultiLineString":
		lines, err := jsonRings(coordinates)
		return MultiLine{Lines: lines}, err
	case "MultiPolygon":
		raw, ok := coordinates.([]any)
		if !ok {
			return nil, fmt.Errorf("tessaridb: a MultiPolygon's coordinates are a list")
		}
		polygons := make([]Polygon, 0, len(raw))
		for _, each := range raw {
			polygon, err := jsonPolygon(each)
			if err != nil {
				return nil, err
			}
			polygons = append(polygons, polygon)
		}
		return MultiPolygon{Polygons: polygons}, nil
	}
	return nil, fmt.Errorf("tessaridb: %q is not a GeoJSON geometry type", kind)
}

// A polygon's rings are a flat list with the exterior first — GeoJSON's own
// shape, and the reason a client's corpus translator meets two notations for one
// polygon (the value corpus names the two parts instead).
func jsonPolygon(j any) (Polygon, error) {
	rings, err := jsonRings(j)
	if err != nil {
		return Polygon{}, err
	}
	if len(rings) == 0 {
		return Polygon{}, nil
	}
	return Polygon{Exterior: rings[0], Interiors: rings[1:]}, nil
}

func jsonRings(j any) ([]Ring, error) {
	raw, ok := j.([]any)
	if !ok {
		return nil, fmt.Errorf("tessaridb: a list of rings, got %T", j)
	}
	rings := make([]Ring, 0, len(raw))
	for _, each := range raw {
		positions, err := jsonPositions(each)
		if err != nil {
			return nil, err
		}
		rings = append(rings, positions)
	}
	return rings, nil
}

func jsonPositions(j any) ([]Position, error) {
	raw, ok := j.([]any)
	if !ok {
		return nil, fmt.Errorf("tessaridb: a list of positions, got %T", j)
	}
	positions := make([]Position, 0, len(raw))
	for _, each := range raw {
		position, err := jsonPosition(each)
		if err != nil {
			return nil, err
		}
		positions = append(positions, position)
	}
	return positions, nil
}

func jsonPosition(j any) (Position, error) {
	pair, ok := j.([]any)
	if !ok || len(pair) < 2 {
		return Position{}, fmt.Errorf("tessaridb: a position is [longitude, latitude]")
	}
	lon, err := jsonCoordinate(pair[0])
	if err != nil {
		return Position{}, err
	}
	lat, err := jsonCoordinate(pair[1])
	return Position{Lon: lon, Lat: lat}, err
}
