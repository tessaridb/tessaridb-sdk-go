package tessaridb

// The value tags. Seventeen types, and a number carries a second byte saying
// which of three kinds it is.
const (
	tagNone     = 0x01
	tagNull     = 0x02
	tagBool     = 0x03
	tagNumber   = 0x04
	tagText     = 0x05
	tagBytes    = 0x06
	tagDuration = 0x07
	tagDatetime = 0x08
	tagUUID     = 0x09
	tagTable    = 0x0a
	tagRecord   = 0x0b
	tagArray    = 0x0c
	tagObject   = 0x0d
	tagRange    = 0x0e
	tagSet      = 0x0f
	tagGeometry = 0x10
	tagRegex    = 0x11
)

const (
	numberInteger = 0x01
	numberFloat   = 0x02
	numberDecimal = 0x03
)

const (
	boundUnbounded = 0x01
	boundIncluded  = 0x02
	boundExcluded  = 0x03
)

const (
	idInteger = 0x01
	idText    = 0x02
	idUUID    = 0x03
	idBytes   = 0x04
)

const (
	shapePoint        = 0x01
	shapeLine         = 0x02
	shapePolygon      = 0x03
	shapeMultiPoint   = 0x04
	shapeMultiLine    = 0x05
	shapeMultiPolygon = 0x06
	shapeCollection   = 0x07
)

const nanosCeiling = 1_000_000_000
