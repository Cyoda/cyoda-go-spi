package spi

import "math/big"

// NumericSubCondition is one expanded numeric branch produced from a single
// numeric operand against a polymorphic (multi-type) field. It mirrors one
// Cloud ParsedCondition (PolymorphicNumberConversions.kt).
//
//   - Value is the (possibly rounded / folded) operand for this type bucket.
//     For int-family branches it is the folded integer as a scale-0 Decimal;
//     for decimal-family branches it is the decimal operand, rounded only when
//     the bucket requires it.
//   - When NotNull is true the branch is a bare existence test (Op is
//     FilterNotNull) and Value is meaningless — the operand fell outside the
//     type's magnitude range in a direction where "is present" is the correct
//     residual predicate.
type NumericSubCondition struct {
	Type    DataType
	Value   Decimal
	Op      FilterOp
	NotNull bool
}

// ExpandNumericOperand expands a single numeric operand into per-type
// sub-conditions for a polymorphic field, faithfully porting Cloud's
// PolymorphicNumberConversions.parseNumberConditionToPolyType
// (PolymorphicNumberConversions.kt:25-36).
//
// declaredNumeric is the field's declared numeric type set. The result is the
// union of the decimal-family and integer-family expansions. An empty result
// means every numeric branch was dropped (the operand contributes nothing —
// "void"): e.g. a fractional value under EQUALS against an integer-only field.
//
// Non-numeric entries in declaredNumeric are ignored (the caller is expected
// to pass numeric types; this guard keeps a stray entry from panicking).
func ExpandNumericOperand(value Decimal, declaredNumeric []DataType, op FilterOp) []NumericSubCondition {
	var intTypes, decTypes []DataType
	for _, t := range declaredNumeric {
		switch NumericFamily(t) {
		case 1:
			intTypes = append(intTypes, t)
		case 2:
			decTypes = append(decTypes, t)
		}
	}
	// Cloud concatenates fltConditions + intConditions (kt:35).
	out := expandDecimalFamily(value, decTypes, op)
	out = append(out, expandIntFamily(value, intTypes, op)...)
	return out
}

// isComparingOp reports whether op is one of the four ordering comparisons
// (the Cloud comparingOperations set — ParserFunctions cited via
// PolymorphicNumberConversions.kt:21-23). EQ is deliberately excluded.
func isComparingOp(op FilterOp) bool {
	return op == FilterGt || op == FilterGte || op == FilterLt || op == FilterLte
}

// isLessOp reports membership in lessComparingOperations {<, <=}.
func isLessOp(op FilterOp) bool { return op == FilterLt || op == FilterLte }

// isGreatOp reports membership in greatComparingOperations {>, >=}.
func isGreatOp(op FilterOp) bool { return op == FilterGt || op == FilterGte }

// roundingModeFor mirrors the CEILING/FLOOR table at
// PolymorphicNumberConversions.kt:130-135: inclusive-for-the-kept-direction
// rounds forward (CEILING) for >= and <, backward (FLOOR) for <= and >, so the
// rounded integer still satisfies the original comparison.
func roundingModeFor(op FilterOp) roundingMode {
	switch op {
	case FilterGte, FilterLt:
		return roundCeiling
	default: // FilterLte, FilterGt
		return roundFloor
	}
}

type rangePos int

const (
	inRangePos rangePos = iota
	abovePos
	belowPos
)

// toRange classifies value against a bucket's [floor, ceiling] magnitude
// window (PolymorphicNumberConversions.kt:76-80). The ceiling test is strict:
// a value equal to the ceiling is IN_RANGE.
func toRange(value, floor, ceiling Decimal) rangePos {
	if value.Cmp(floor) < 0 {
		return belowPos
	}
	if value.Cmp(ceiling) > 0 {
		return abovePos
	}
	return inRangePos
}

// decimalFromBigInt wraps a big.Int as a scale-0 Decimal.
func decimalFromBigInt(b *big.Int) Decimal {
	return Decimal{unscaled: new(big.Int).Set(b), scale: 0}
}

// neg returns -d.
func (d Decimal) neg() Decimal {
	if d.unscaled == nil {
		return Decimal{unscaled: new(big.Int), scale: d.scale}
	}
	return Decimal{unscaled: new(big.Int).Neg(d.unscaled), scale: d.scale}
}

// mustDecimalConst parses a compile-time-known decimal for package init.
func mustDecimalConst(s string) Decimal {
	d, err := ParseDecimal(s)
	if err != nil {
		panic("numeric_bucket: bad constant " + s + ": " + err.Error())
	}
	return d
}

// Decimal-family bucket bounds (PolymorphicNumberConversions.kt:163-187,
// ParserFunctions.kt:28-47). DOUBLE uses ±typeMaxValue(15,292); BIG_DECIMAL
// uses ±INT128/10^18 (magnitude only).
var (
	// doubleMax = 9.<14 nines>e292 = typeMaxValue(15, 292), stripped.
	doubleBucketMax = mustDecimalConst("9.99999999999999e292").StripTrailingZeros()
	// bd128Max = INT128_MAX / 10^18 = Decimal{int128Max, scale 18}.
	bd128Max = Decimal{unscaled: new(big.Int).Set(int128Max), scale: 18}
	bd128Min = Decimal{unscaled: new(big.Int).Set(int128Min), scale: 18}
)

// expandDecimalFamily ports fltConversions.valueToParsedConditions
// (PolymorphicNumberConversions.kt:40-64, 163-187). The UNBOUND_DECIMAL sink,
// when declared, always emits the operand verbatim.
func expandDecimalFamily(value Decimal, decTypes []DataType, op FilterOp) []NumericSubCondition {
	if len(decTypes) == 0 {
		return nil
	}
	var out []NumericSubCondition
	for _, t := range decTypes {
		if t == UnboundDecimal {
			// Default sink: verbatim (value, op), no range check, no rounding.
			out = append(out, NumericSubCondition{Type: UnboundDecimal, Value: value, Op: op})
			continue
		}
		var floor, ceiling Decimal
		switch t {
		case Double:
			floor, ceiling = doubleBucketMax.neg(), doubleBucketMax
		case BigDecimal:
			floor, ceiling = bd128Min, bd128Max
		default:
			continue // unreachable for numeric family-2 types
		}
		switch toRange(value, floor, ceiling) {
		case inRangePos:
			if c, ok := produceDecimalInRange(t, value, op); ok {
				out = append(out, c)
			}
		case abovePos:
			if isLessOp(op) {
				out = append(out, NumericSubCondition{Type: t, Op: FilterNotNull, NotNull: true})
			}
		case belowPos:
			if isGreatOp(op) {
				out = append(out, NumericSubCondition{Type: t, Op: FilterNotNull, NotNull: true})
			}
		}
	}
	return out
}

// produceDecimalInRange ports FltConverter.produceInRangeCondition for the
// decimal buckets (PolymorphicNumberConversions.kt:117-162). Returns ok=false
// to drop the branch (imprecise value under a non-comparing op such as EQ).
func produceDecimalInRange(t DataType, value Decimal, op FilterOp) (NumericSubCondition, bool) {
	switch t {
	case BigDecimal:
		// BIG_DECIMAL bucket is magnitude-only with isPrecise ≡ true and no
		// rounding (PolymorphicNumberConversions.kt:178-186): the scale≤18
		// restriction is a Trino storage constraint, irrelevant to a search
		// condition, so a high-scale-but-in-magnitude value that
		// ParseStringOrNull(BIG_DECIMAL) would reject is still emitted here
		// verbatim as a BIG_DECIMAL condition.
		return NumericSubCondition{Type: BigDecimal, Value: value, Op: op}, true
	case Double:
		if isDoubleBucketPrecise(value) {
			// Precise: no rounding, no op change.
			return NumericSubCondition{Type: Double, Value: value, Op: op}, true
		}
		if isComparingOp(op) {
			return NumericSubCondition{Type: Double, Value: roundDoubleImprecise(value, roundingModeFor(op)), Op: op}, true
		}
		// Imprecise EQUALS on a type bucket: drop (kt:138).
		return NumericSubCondition{}, false
	default:
		return NumericSubCondition{}, false
	}
}

// isDoubleBucketPrecise mirrors getRoundingFltConverter's isPrecise for DOUBLE
// (PolymorphicNumberConversions.kt:148-162, 171-176): precision ≤ 15 AND scale
// ≤ 292. Note this is scale ≤ maxScale (not |scale|) — negative scales are
// always precise. This is intentionally distinct from the DOUBLE *envelope*
// classifier isDoubleEnvelope (which bounds |scale|); this predicate decides
// only whether the bucket must round, not whether the value classifies as
// DOUBLE.
func isDoubleBucketPrecise(value Decimal) bool {
	return value.Precision() <= doubleMaxPrecision && int(value.Scale()) <= doubleMaxAbsScale
}

// roundDoubleImprecise ports getRoundingFltConverter's rounding lambda
// (PolymorphicNumberConversions.kt:152-160): first clamp scale to maxScale
// (292), then, if precision still exceeds maxPrecision (15), round the
// *original* value to 15 significant digits — faithful to the Cloud code,
// which rounds `value` (not the scale-clamped intermediate) in the second step.
func roundDoubleImprecise(value Decimal, mode roundingMode) Decimal {
	result := value
	if result.Scale() > doubleMaxAbsScale {
		result = value.roundToScale(doubleMaxAbsScale, mode)
	}
	if result.Precision() > doubleMaxPrecision {
		result = value.roundToPrecision(doubleMaxPrecision, mode)
	}
	return result
}

// Integer-family bucket bounds (PolymorphicNumberConversions.kt:91-116).
// BYTE/SHORT are dropped in cyoda-go (spec §2.3).
var (
	intBoundInteger32Min = decimalFromBigInt(classifyInt32Min)
	intBoundInteger32Max = decimalFromBigInt(classifyInt32Max)
	intBoundLong64Min    = decimalFromBigInt(classifyInt64Min)
	intBoundLong64Max    = decimalFromBigInt(classifyInt64Max)
	intBound128Min       = decimalFromBigInt(int128Min)
	intBound128Max       = decimalFromBigInt(int128Max)
)

// expandIntFamily ports the int-family half of parseNumberConditionToPolyType
// (PolymorphicNumberConversions.kt:28-34) plus intConversions.
// valueToParsedConditions. The operand is folded to a BigInteger exactly once
// (fltToIntConverter), and that single folded value + operation drives every
// int bucket including the UNBOUND_INTEGER sink.
func expandIntFamily(value Decimal, intTypes []DataType, op FilterOp) []NumericSubCondition {
	if len(intTypes) == 0 {
		return nil
	}
	intVal, intOp, ok := foldToInt(value, op)
	if !ok {
		// Fractional value under a non-comparing op → fold yields null →
		// the entire int family is dropped (kt:34, :138).
		return nil
	}
	var out []NumericSubCondition
	for _, t := range intTypes {
		if t == UnboundInteger {
			out = append(out, NumericSubCondition{Type: UnboundInteger, Value: intVal, Op: intOp})
			continue
		}
		var floor, ceiling Decimal
		switch t {
		case Integer:
			floor, ceiling = intBoundInteger32Min, intBoundInteger32Max
		case Long:
			floor, ceiling = intBoundLong64Min, intBoundLong64Max
		case BigInteger:
			floor, ceiling = intBound128Min, intBound128Max
		default:
			continue
		}
		switch toRange(intVal, floor, ceiling) {
		case inRangePos:
			// IntConverter.produceInRangeCondition never mutates the value or
			// op (kt:84-88) — only Java's narrow cast, which is a no-op for us.
			out = append(out, NumericSubCondition{Type: t, Value: intVal, Op: intOp})
		case abovePos:
			if isLessOp(intOp) {
				out = append(out, NumericSubCondition{Type: t, Op: FilterNotNull, NotNull: true})
			}
		case belowPos:
			if isGreatOp(intOp) {
				out = append(out, NumericSubCondition{Type: t, Op: FilterNotNull, NotNull: true})
			}
		}
	}
	return out
}

// foldToInt ports fltToIntConverter.produceInRangeCondition
// (PolymorphicNumberConversions.kt:142-147 with the FltConverter body at
// :119-140). A whole value (scale ≤ 0) folds exactly, keeping the operation.
// A fractional value under a comparing op rounds to an integer via the
// CEILING/FLOOR table, keeping the operation. A fractional value under any
// other op (EQ) returns ok=false — the caller drops the int family entirely.
// The returned Decimal always has scale 0.
func foldToInt(value Decimal, op FilterOp) (Decimal, FilterOp, bool) {
	if value.Scale() <= 0 {
		// Whole number (toBigInteger). SetScale(0) is exact upward for
		// negative scale and identity for scale 0.
		normalized, err := value.SetScale(0)
		if err != nil {
			// Unreachable: scale ≤ 0 is always an exact upward rescale.
			panic("foldToInt: SetScale(0) failed on whole value: " + err.Error())
		}
		return normalized, op, true
	}
	if isComparingOp(op) {
		return value.roundToScale(0, roundingModeFor(op)), op, true
	}
	return Decimal{}, "", false
}
