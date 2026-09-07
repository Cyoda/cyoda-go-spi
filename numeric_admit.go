package spi

// AdmitsNumeric reports whether a field declaring t can hold the value v.
//
// This is the single definition of numeric admission. Ingestion asks it to
// decide whether a write changes the model; the search kernel asks it to
// decide whether a stored value belongs to the declared type. Two copies
// would be two things that can disagree, which is the defect this predicate
// exists to remove.
//
// It is deliberately NOT "is v inside t's range". Admission must agree with
// what search can find, and for DOUBLE the operand bucket drops the EQUALS
// branch entirely for a value that exceeds 15 significant digits or a scale
// of 292 (see produceDecimalInRange / isDoubleBucketPrecise) — so a value
// admitted on range alone would be stored where EQUALS could never find it
// and NOT_EQUAL would wrongly match it. The integer family needs wholeness on
// top of its bounds for the same reason: foldToInt drops the whole family for
// a fractional operand, and UNBOUND_INTEGER has no bound to hide behind.
//
// The precision bound is also the mantissa argument stated as a value test
// rather than a label test: a decimal of at most 15 significant digits
// round-trips uniquely through a binary64 double, which is exactly what the
// DOUBLE bucket's findability and the lossless float8 pushdown need.
// 2147483648 (10 digits) is inside that bound; 9007199254740993 (16) is
// not — without condemning the 10-digit value by association with its LONG
// label. This is not "precision <= 15 excludes exactly the values above
// 2^53": the predicate works on stripped precision, so a value like 1e16
// strips to precision 1 and is admitted despite being past 2^53 — the bound
// is on significant digits, not on magnitude.
//
// A non-numeric t is never admitted; callers route by JSON kind first.
func AdmitsNumeric(t DataType, v Decimal) bool {
	if !IsNumeric(t) {
		return false
	}
	stripped := v.StripTrailingZeros()

	switch t {
	case UnboundDecimal:
		// The lattice sink: no bound, no precision limit, emitted verbatim by
		// expandDecimalFamily.
		return true

	case UnboundInteger:
		// No bound, but foldToInt still drops a fractional value.
		return stripped.Scale() <= 0

	case Integer:
		return admitsWholeInRange(stripped, intBoundInteger32Min, intBoundInteger32Max)
	case Long:
		return admitsWholeInRange(stripped, intBoundLong64Min, intBoundLong64Max)
	case BigInteger:
		return admitsWholeInRange(stripped, intBound128Min, intBound128Max)

	case Double:
		// Range AND the bucket's precision test. Both conjuncts are
		// load-bearing; see the doc comment above.
		if toRange(stripped, doubleBucketMax.neg(), doubleBucketMax) != inRangePos {
			return false
		}
		return isDoubleBucketPrecise(stripped)

	case BigDecimal:
		// Magnitude only. produceDecimalInRange documents the scale <= 18
		// restriction as a Trino storage constraint irrelevant to a search
		// condition, and emits a high-scale in-magnitude value verbatim.
		return toRange(stripped, bd128Min, bd128Max) == inRangePos

	default:
		return false
	}
}

// admitsWholeInRange is the integer-family rule: whole after stripping, and
// inside the type's bounds.
func admitsWholeInRange(stripped, floor, ceiling Decimal) bool {
	if stripped.Scale() > 0 {
		return false
	}
	return toRange(stripped, floor, ceiling) == inRangePos
}
