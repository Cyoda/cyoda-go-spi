package spi

import (
	"regexp"

	"github.com/google/uuid"
)

// uuidPattern is Cloud's lowercase RFC UUID regex
// (ValueDetectionFunctions.kt:22-25). Uppercase hex digits are
// deliberately rejected — case-sensitivity matches Cloud's detector.
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ParseStringOrNull parses operand as the target DataType t, porting
// Cloud's DataType.parseStringOrNull (DataType.kt:125-166). ok=false means
// operand does not parse as t — this is never an error; callers drop that
// type-branch from a polymorphic evaluation and try the next candidate
// type.
//
// Temporal types (LocalDate, LocalDateTime, LocalTime, ZonedDateTime, Year,
// YearMonth) are NOT handled here: ParseStringOrNull always returns
// (nil, false) for them. The temporal engine (a later task) parses those
// separately.
//
// Successful numeric parses (whole or decimal) return a spi.Decimal — it
// losslessly represents both integral and fractional values via
// unscaled/scale, so a single return type covers every numeric DataType
// without an integer/decimal split in the result shape.
func ParseStringOrNull(operand string, t DataType) (any, bool) {
	switch t {
	case Integer, Long, BigInteger, UnboundInteger:
		return parseWholeType(operand, t)
	case Double, BigDecimal, UnboundDecimal:
		return parseDecimalType(operand, t)
	case Boolean:
		return parseBoolean(operand)
	case String:
		return operand, true
	case Character:
		return parseCharacter(operand)
	case UUIDType, TimeUUIDType:
		return parseUUIDValue(operand, t)
	default:
		// Temporal types, ByteArray, Null: not handled here.
		return nil, false
	}
}

// int128MaxDigits is the decimal digit count of INT128_MAX
// (170141183460469231731687303715884105727) and of INT128_MIN's magnitude —
// the widest bounded integer type there is. A whole value with more digits
// than this is outside INTEGER, LONG and BIG_INTEGER alike, whatever its
// sign, so its digit count alone settles the parse.
const int128MaxDigits = 39

// parseWholeType implements DataType.kt:132-137 / NumberParsing.kt:29-50:
// parse as Decimal, strip trailing zeros, require integral (scale <= 0),
// then range-check the integer value against the target type's width.
func parseWholeType(operand string, t DataType) (any, bool) {
	d, err := ParseDecimal(operand)
	if err != nil {
		return nil, false
	}
	d = d.StripTrailingZeros()
	if d.Scale() > 0 {
		return nil, false // fractional — not a whole number
	}
	// The unbound sink keeps the stripped form, negative scale and all: it
	// is already a whole number, it has no range to check, and every
	// consumer reads it through the magnitude-first Cmp, which compares any
	// two scales without materialising either. Normalising "1e2000000000"
	// would buy a two-billion-digit coefficient nobody inspects — foldToInt
	// declines the same normalisation on the same shape for the same reason.
	if t == UnboundInteger {
		return d, true
	}
	if d.Scale() < 0 {
		// e.g. "1E2" strips to unscaled=1, scale=-2 (value 100). Normalize
		// to scale 0 so Unscaled() reflects the true integer value — but
		// only once the digit count says the value could still land in a
		// bounded range. The whole number has Precision() + (-Scale())
		// digits, and past int128MaxDigits it is outside INTEGER, LONG and
		// BIG_INTEGER alike, so the answer is (nil, false) with nothing
		// built. SetScale would otherwise materialise 10^(-Scale()) first,
		// and that exponent is bounded only by int32: "1e40000000" spent
		// 8.5 s here, and "1e2000000000" the same minutes and hundreds of
		// megabytes the numeric-bucket rounding used to. Past the guard the
		// materialisation is at most int128MaxDigits digits wide.
		if int64(d.Precision())-int64(d.Scale()) > int128MaxDigits {
			return nil, false
		}
		d, err = d.SetScale(0)
		if err != nil {
			return nil, false
		}
	}
	v := d.Unscaled()

	switch t {
	case Integer:
		if v.Cmp(classifyInt32Min) >= 0 && v.Cmp(classifyInt32Max) <= 0 {
			return d, true
		}
	case Long:
		if v.Cmp(classifyInt64Min) >= 0 && v.Cmp(classifyInt64Max) <= 0 {
			return d, true
		}
	case BigInteger:
		if d.IsInt128() {
			return d, true
		}
	}
	return nil, false
}

// parseDecimalType implements DataType.kt:140-143 / NumberParsing.kt:70-73
// / ParserFunctions.kt:35-59: parse as Decimal, strip trailing zeros, then
// check the target type's own numeric envelope directly (not the
// narrowest-fit classification — a value can satisfy DOUBLE's envelope
// without satisfying BIG_DECIMAL's, and vice versa).
func parseDecimalType(operand string, t DataType) (any, bool) {
	d, err := ParseDecimal(operand)
	if err != nil {
		return nil, false
	}
	d = d.StripTrailingZeros()

	switch t {
	case Double:
		if isDoubleEnvelope(d) {
			return d, true
		}
	case BigDecimal:
		if isInt128Decimal(d) {
			return d, true
		}
	case UnboundDecimal:
		return d, true
	}
	return nil, false
}

// parseBoolean implements DataType.kt:145: exactly "true" or "false",
// case-sensitive.
func parseBoolean(operand string) (any, bool) {
	switch operand {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return nil, false
	}
}

// parseCharacter requires operand to be exactly one rune.
func parseCharacter(operand string) (any, bool) {
	runes := []rune(operand)
	if len(runes) != 1 {
		return nil, false
	}
	return runes[0], true
}

// parseUUIDValue implements ValueDetectionFunctions.kt:22-25: the operand
// must match the lowercase RFC UUID pattern; TimeUUIDType additionally
// requires version 1.
func parseUUIDValue(operand string, t DataType) (any, bool) {
	if !uuidPattern.MatchString(operand) {
		return nil, false
	}
	id, err := uuid.Parse(operand)
	if err != nil {
		return nil, false
	}
	if t == TimeUUIDType && id.Version() != 1 {
		return nil, false
	}
	return id, true
}
