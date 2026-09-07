package spi

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Decimal is a fixed-scale arbitrary-precision decimal.
// Value = unscaled × 10^(-scale).
// Scale may be negative (e.g. 1e2 has unscaled=1, scale=-2).
// No arithmetic — cyoda-go delegates arithmetic to Trino.
type Decimal struct {
	unscaled *big.Int
	scale    int32
}

// ParseDecimal parses a decimal string. Accepts integer literals,
// fractional literals, and scientific notation with optional sign.
// Rejects NaN, Infinity, empty strings, and malformed forms.
func ParseDecimal(s string) (Decimal, error) {
	if s == "" {
		return Decimal{}, fmt.Errorf("parse decimal: empty string")
	}
	switch strings.ToLower(s) {
	case "nan", "inf", "infinity", "+inf", "+infinity", "-inf", "-infinity":
		return Decimal{}, fmt.Errorf("parse decimal: non-numeric token %q", s)
	}

	// Split mantissa and exponent.
	var mantissa, expPart string
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa, expPart = s[:i], s[i+1:]
		if expPart == "" {
			return Decimal{}, fmt.Errorf("parse decimal: empty exponent in %q", s)
		}
	} else {
		mantissa = s
	}
	if mantissa == "" || mantissa == "+" || mantissa == "-" {
		return Decimal{}, fmt.Errorf("parse decimal: empty mantissa in %q", s)
	}

	// Strip mantissa sign for easier processing; remember it.
	sign := ""
	switch mantissa[0] {
	case '+':
		mantissa = mantissa[1:]
	case '-':
		sign = "-"
		mantissa = mantissa[1:]
	}
	if mantissa == "" {
		return Decimal{}, fmt.Errorf("parse decimal: missing digits in %q", s)
	}

	// Split integer and fractional parts.
	var intPart, fracPart string
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		intPart, fracPart = mantissa[:i], mantissa[i+1:]
		if strings.ContainsRune(fracPart, '.') {
			return Decimal{}, fmt.Errorf("parse decimal: multiple decimal points in %q", s)
		}
	} else {
		intPart = mantissa
	}

	// If intPart is empty (e.g. ".5"), use "0".
	if intPart == "" {
		intPart = "0"
	}

	// Reject bare "." / "-." / "+." — after the sign-strip and
	// intPart="" → "0" substitution above, these would otherwise parse
	// as zero. Malformed input should error, not silently become 0.
	if intPart == "0" && fracPart == "" && strings.ContainsRune(mantissa, '.') {
		return Decimal{}, fmt.Errorf("parse decimal: no digits in %q", s)
	}

	// Validate digits.
	for _, r := range intPart {
		if r < '0' || r > '9' {
			return Decimal{}, fmt.Errorf("parse decimal: invalid digit %q in %q", r, s)
		}
	}
	for _, r := range fracPart {
		if r < '0' || r > '9' {
			return Decimal{}, fmt.Errorf("parse decimal: invalid digit %q in %q", r, s)
		}
	}

	// Parse exponent.
	var exp int64 = 0
	if expPart != "" {
		var err error
		exp, err = strconv.ParseInt(expPart, 10, 64)
		if err != nil {
			return Decimal{}, fmt.Errorf("parse decimal: invalid exponent %q: %w", expPart, err)
		}
	}

	// Build unscaled: sign + intPart + fracPart.
	unscaledStr := sign + intPart + fracPart
	unscaled, ok := new(big.Int).SetString(unscaledStr, 10)
	if !ok {
		return Decimal{}, fmt.Errorf("parse decimal: failed to build unscaled from %q", s)
	}

	// Scale: fractional-digit count minus exponent.
	scale := int64(len(fracPart)) - exp
	if scale > math.MaxInt32 || scale < math.MinInt32 {
		return Decimal{}, fmt.Errorf("parse decimal: scale %d out of int32 range", scale)
	}
	return Decimal{unscaled: unscaled, scale: int32(scale)}, nil
}

// IsZero reports whether d is numerically zero.
func (d Decimal) IsZero() bool {
	return d.unscaled != nil && d.unscaled.Sign() == 0
}

// Sign returns -1 for negative, 0 for zero, 1 for positive.
func (d Decimal) Sign() int {
	if d.unscaled == nil {
		return 0
	}
	return d.unscaled.Sign()
}

// Scale returns the scale: number of digits after the decimal point.
// Negative scale corresponds to scientific notation like 1e2.
func (d Decimal) Scale() int32 {
	return d.scale
}

// Unscaled returns a defensive copy of the unscaled big.Int.
func (d Decimal) Unscaled() *big.Int {
	if d.unscaled == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(d.unscaled)
}

// StripTrailingZeros returns a Decimal with trailing zeros removed
// from the unscaled value. Matches Java BigDecimal.stripTrailingZeros
// semantics: a non-zero unscaled value with trailing zero digits has
// those digits removed and the scale decremented accordingly. A zero
// value collapses to unscaled=0, scale=0.
//
// The whole zero run is removed in one division. Peeling one digit per
// full-width QuoRem, as this once did, costs O(zeros × digits): a
// 1,000,002-byte operand — a "1" and a million zeros, comfortably inside
// the request body cap — took minutes. Both the search-operand path
// (expandCompare strips the operand before bucketing it) and the write
// path (inferDataType) are request-boundary, so the cost has to be the
// value's own size and nothing else.
func (d Decimal) StripTrailingZeros() Decimal {
	if d.unscaled == nil || d.unscaled.Sign() == 0 {
		return Decimal{unscaled: new(big.Int), scale: 0}
	}
	// An odd coefficient ends in an odd decimal digit, so it has no
	// trailing zero at all — the common case, settled in O(1) without
	// rendering anything.
	if d.unscaled.Bit(0) == 1 {
		return Decimal{unscaled: new(big.Int).Set(d.unscaled), scale: d.scale}
	}
	// One decimal rendering — big.Int's conversion is divide-and-conquer,
	// so this is the cheap way to learn the zero count — then one division.
	// The rendering may carry a leading '-'; counting from the end is
	// unaffected by it, and the run can never consume every digit because
	// a non-zero value has a non-zero leading digit.
	s := d.unscaled.String()
	k := int64(0)
	for i := len(s) - 1; i >= 0 && s[i] == '0'; i-- {
		k++
	}
	// Refusing to wrap is the only safe answer; a value at the int32 scale
	// floor cannot be stripped further and returning it unstripped is
	// exact. Strip only as far as the floor allows — exactly where the
	// per-digit loop stopped.
	if headroom := int64(d.scale) - math.MinInt32; k > headroom {
		k = headroom
	}
	if k == 0 {
		return Decimal{unscaled: new(big.Int).Set(d.unscaled), scale: d.scale}
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(k), nil)
	return Decimal{unscaled: new(big.Int).Quo(d.unscaled, factor), scale: d.scale - int32(k)}
}

// Precision returns the number of significant digits in the unscaled
// value. Matches Java BigDecimal.precision() — returns 1 for zero.
func (d Decimal) Precision() int {
	if d.unscaled == nil || d.unscaled.Sign() == 0 {
		return 1
	}
	abs := new(big.Int).Abs(d.unscaled)
	return len(abs.String())
}

// SetScale returns a Decimal at the requested scale. Upward scale
// (adding fractional digits) multiplies the unscaled value by
// 10^(n-scale) and always succeeds. Downward scale (removing
// fractional digits) succeeds only if the unscaled value is divisible
// by 10^(scale-n); otherwise returns a precision-loss error.
func (d Decimal) SetScale(newScale int32) (Decimal, error) {
	if d.scale == newScale {
		u := new(big.Int)
		if d.unscaled != nil {
			u.Set(d.unscaled)
		}
		return Decimal{unscaled: u, scale: newScale}, nil
	}
	// Zero rescales exactly in either direction, and answering it here keeps
	// a scale gap bounded only by int32 from materialising a power of ten
	// that could not have changed the result. A nil coefficient reads as
	// zero, as it does in Sign, IsZero and Unscaled.
	if d.unscaled == nil || d.unscaled.Sign() == 0 {
		return Decimal{unscaled: new(big.Int), scale: newScale}, nil
	}
	diff := int64(newScale) - int64(d.scale)
	if diff > 0 {
		factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(diff), nil)
		u := new(big.Int).Mul(d.unscaled, factor)
		return Decimal{unscaled: u, scale: newScale}, nil
	}
	// diff < 0: divide by 10^(-diff); require exactness.
	//
	// Settle the inexact case before building the divisor — the scale gap is
	// bounded only by int32, so 10^(-diff) is a multi-hundred-megabyte
	// integer for an operand as small as "1e-2000000000". A non-zero
	// coefficient of p digits satisfies |unscaled| < 10^p, so as soon as
	// -diff > p the divisor strictly exceeds it and the division is inexact.
	if -diff > int64(d.Precision()) {
		return Decimal{}, fmt.Errorf("SetScale: cannot reduce scale from %d to %d without precision loss", d.scale, newScale)
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(-diff), nil)
	q := new(big.Int)
	r := new(big.Int)
	q.QuoRem(d.unscaled, factor, r)
	if r.Sign() != 0 {
		return Decimal{}, fmt.Errorf("SetScale: cannot reduce scale from %d to %d without precision loss", d.scale, newScale)
	}
	return Decimal{unscaled: q, scale: newScale}, nil
}

// roundingMode selects the direction for the integer/scale rounding
// helpers below. SetScale is exact-only (errors on precision loss); these
// helpers add the rounding that the numeric-bucket engine needs.
type roundingMode int

const (
	// roundCeiling rounds toward +∞.
	roundCeiling roundingMode = iota
	// roundFloor rounds toward -∞.
	roundFloor
)

// roundToScale returns d rounded to newScale using mode.
//
// Upward rescale (newScale >= d.scale) is exact and never rounds — it
// delegates to SetScale. Downward rescale divides the unscaled value by
// 10^(d.scale-newScale) via big.Int QuoRem, which truncates toward zero;
// the remainder therefore carries the sign of the dividend. The sign-aware
// adjustment below turns that truncation into a true CEILING (+∞) or FLOOR
// (-∞): for CEILING bump the quotient up only when the dropped part is
// positive; for FLOOR bump it down only when the dropped part is negative.
func (d Decimal) roundToScale(newScale int32, mode roundingMode) Decimal {
	if d.unscaled == nil {
		return Decimal{unscaled: new(big.Int), scale: newScale}
	}
	if newScale >= d.scale {
		r, err := d.SetScale(newScale) // exact upward — never loses precision
		if err != nil {
			panic(fmt.Sprintf("roundToScale: upward SetScale failed: %v", err))
		}
		return r
	}
	// Below the operand's own precision the answer is arithmetic, not
	// division. With d = u × 10^(-s), u ≠ 0 and p = Precision() digits,
	// |u| < 10^p; rescaling down to n < s divides by 10^(s-n), so once
	// s-n > p the divisor strictly exceeds |u| and the truncated quotient is
	// 0 with the whole (non-zero) value as the dropped part. CEILING then
	// yields +1 for a positive d and 0 for a negative one; FLOOR mirrors it.
	// Building 10^(s-n) instead costs minutes and hundreds of megabytes for
	// a 13-byte operand — "-1e-2000000000" reaches here through foldToInt
	// (newScale 0) and roundDoubleImprecise (newScale 292) alike. Below the
	// threshold the divisor is bounded by the operand's own digits, so the
	// general path below stands.
	diff := int64(d.scale) - int64(newScale) // > 0
	sign := d.unscaled.Sign()
	if sign == 0 {
		return Decimal{unscaled: new(big.Int), scale: newScale}
	}
	if diff > int64(d.Precision()) {
		u := new(big.Int)
		switch {
		case mode == roundCeiling && sign > 0:
			u.SetInt64(1)
		case mode == roundFloor && sign < 0:
			u.SetInt64(-1)
		}
		return Decimal{unscaled: u, scale: newScale}
	}
	factor := new(big.Int).Exp(big.NewInt(10), big.NewInt(diff), nil)
	q := new(big.Int)
	rem := new(big.Int)
	q.QuoRem(d.unscaled, factor, rem)
	if rem.Sign() != 0 {
		switch mode {
		case roundCeiling:
			if rem.Sign() > 0 {
				q.Add(q, big.NewInt(1))
			}
		case roundFloor:
			if rem.Sign() < 0 {
				q.Sub(q, big.NewInt(1))
			}
		}
	}
	return Decimal{unscaled: q, scale: newScale}
}

// roundToPrecision rounds d to at most maxPrec significant digits using
// mode. It mirrors Java BigDecimal.round(MathContext(maxPrec, mode)) for
// the cases the DOUBLE bucket needs: when the current precision already
// fits it is a no-op, otherwise it rounds at the scale that leaves maxPrec
// significant digits.
//
// A carry can grow the rounded result past maxPrec digits (e.g. 9.99 → 10 —
// the dropped-digit rounding turns a run of 9s into a leading 1, adding one
// digit of precision). When that happens the result is renormalized back to
// maxPrec digits by reducing its scale by one more via an exact SetScale —
// exact because the carry that caused the overflow always leaves the
// unscaled value ending in a zero. This mirrors the renormalization Java's
// BigDecimal.round(MathContext) performs at the same boundary.
func (d Decimal) roundToPrecision(maxPrec int, mode roundingMode) Decimal {
	p := d.Precision()
	if p <= maxPrec {
		u := new(big.Int)
		if d.unscaled != nil {
			u.Set(d.unscaled)
		}
		return Decimal{unscaled: u, scale: d.scale}
	}
	drop := int32(p - maxPrec)
	result := d.roundToScale(d.scale-drop, mode)
	if result.Precision() > maxPrec {
		renorm, err := result.SetScale(result.scale - 1)
		if err != nil {
			panic(fmt.Sprintf("roundToPrecision: renormalize carry failed: %v", err))
		}
		result = renorm
	}
	return result
}

// log10(2) · 2^32 = 1292913986.3546…, so
//
//	log10Of2Lo / 2^32  <  log10(2)  <  log10Of2Hi / 2^32
//
// strictly, in both directions. Dyadic denominators keep the arithmetic exact
// in int64: the largest product is bitLen · log10Of2Hi, and for bitLen ≤
// math.MaxInt32 that is under 2.78e18, well inside int64.
const (
	log10Of2Lo    = 1292913986
	log10Of2Hi    = 1292913987
	log10Of2Shift = 32
)

// digitCountBounds brackets the number of decimal digits in a non-zero
// big.Int of bitLen bits: lo <= digits <= hi, with hi-lo <= 1.
//
// For a value v with bit length b, 2^(b-1) <= |v| <= 2^b − 1, so the digit
// count D = floor(log10|v|) + 1 satisfies
//
//	D >= floor((b-1)·log10 2) + 1 >= floor((b-1)·log10Of2Lo/2^32) + 1 = lo
//	D <= floor(log10(2^b − 1)) + 1 <= floor(b·log10 2) + 1
//	                                <= floor(b·log10Of2Hi/2^32) + 1 = hi
//
// (x > y implies floor(x) >= floor(y), which is what carries each bound
// across the rational approximation).
//
// The width is at most one digit: hi − lo <= (b·log10Of2Hi − (b−1)·log10Of2Lo)/2^32
// + 1 = (b + log10Of2Lo)/2^32 + 1, and for every b <= math.MaxInt32 that is
// (2147483647 + 1292913986)/2^32 + 1 = 1.801…; hi − lo is an integer, so it is
// at most 1.
func digitCountBounds(bitLen int) (lo, hi int64) {
	if bitLen <= 0 {
		return 1, 1 // zero has one digit, matching Precision()
	}
	b := int64(bitLen)
	return ((b-1)*log10Of2Lo)>>log10Of2Shift + 1, (b*log10Of2Hi)>>log10Of2Shift + 1
}

// int128Min = -2^127, int128Max = 2^127 - 1.
// Pre-computed once at package init to avoid recomputing per call.
var int128Min = new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 127))
var int128Max = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 127), big.NewInt(1))

// Cmp returns -1 if d < other, 0 if equal, 1 if d > other. Exact — no
// rounding modes.
//
// Magnitude first. The sign decides when the signs differ; the adjusted
// exponent — precision − scale, the position of the most significant
// digit — decides when they do not. Only a tie on both needs the
// coefficients aligned to a common scale, and in a tie the scale
// difference equals the precision difference, so the alignment cost is
// bounded by the operands' own digit counts.
//
// Aligning unconditionally, as this once did, multiplied the smaller-scale
// coefficient by 10^diff — and diff is bounded only by int32, so comparing
// 1e10000000 against any ordinary value materialised a ten-million-digit
// integer. That was reachable from a 13-byte search operand through
// toRange before any other guard ran.
func (d Decimal) Cmp(other Decimal) int {
	ds, os := d.Sign(), other.Sign()
	if ds != os {
		if ds < os {
			return -1
		}
		return 1
	}
	if ds == 0 {
		return 0
	}

	// Equal scale: the coefficients are already aligned, so comparing them
	// directly is exact and skips the adjusted-exponent computation (two
	// Precision() calls, each a string conversion) entirely. The common
	// case on a per-row scan loop, where every value in a column shares one
	// scale.
	if d.scale == other.scale {
		return d.unscaled.Cmp(other.unscaled)
	}

	// Same non-zero sign: compare magnitudes. For negatives the larger
	// magnitude is the smaller value, which multiplying by the sign
	// handles.
	//
	// Try the bit-length bracket first. Precision() is a full big.Int → string
	// conversion — 20 ms at 300,000 digits, and this runs per row in
	// evalCompare/evalBetween whenever an UNBOUND-sink operand's scale differs
	// from the stored value's. Bit length is O(1) and pins the digit count to
	// within one, which decides the order outright unless the two adjusted
	// exponents are genuinely close.
	dLo, dHi := digitCountBounds(d.unscaled.BitLen())
	oLo, oHi := digitCountBounds(other.unscaled.BitLen())
	switch {
	case dLo-int64(d.scale) > oHi-int64(other.scale):
		return ds
	case dHi-int64(d.scale) < oLo-int64(other.scale):
		return -ds
	}

	// The brackets overlap: pay for the exact digit counts.
	dAdj := int64(d.Precision()) - int64(d.scale)
	oAdj := int64(other.Precision()) - int64(other.scale)
	switch {
	case dAdj > oAdj:
		return ds
	case dAdj < oAdj:
		return -ds
	}

	// Tie on magnitude: align scales, bounded by the precision gap.
	target := d.scale
	if other.scale > target {
		target = other.scale
	}
	dAligned, err := d.SetScale(target)
	if err != nil {
		// Unreachable: upward SetScale always succeeds.
		panic(fmt.Sprintf("Decimal.Cmp: upward SetScale failed: %v", err))
	}
	oAligned, err := other.SetScale(target)
	if err != nil {
		panic(fmt.Sprintf("Decimal.Cmp: upward SetScale failed: %v", err))
	}
	return dAligned.unscaled.Cmp(oAligned.unscaled)
}

// IsInt128 reports whether the unscaled value fits the signed Int128
// range [-2^127, 2^127-1]. Scale is not considered.
//
// Implementation note: relies on pre-computed boundaries rather than
// big.Int.BitLen() comparisons, because BitLen ignores sign and
// BitLen(-2^127) == 128 — incorrectly excluding the valid minimum.
func (d Decimal) IsInt128() bool {
	if d.unscaled == nil {
		return true
	}
	return d.unscaled.Cmp(int128Min) >= 0 && d.unscaled.Cmp(int128Max) <= 0
}

// Canonical returns a plain-decimal string representation (no
// scientific notation). Round-trippable through ParseDecimal.
func (d Decimal) Canonical() string {
	if d.unscaled == nil || d.unscaled.Sign() == 0 {
		return "0"
	}
	unscaledStr := d.unscaled.String() // includes leading "-" if negative
	neg := false
	digits := unscaledStr
	if unscaledStr[0] == '-' {
		neg = true
		digits = unscaledStr[1:]
	}
	var result string
	switch {
	case d.scale == 0:
		result = digits
	case d.scale > 0:
		// Insert decimal point (len(digits) - scale) from the left;
		// pad with leading zeros if needed.
		pad := int(d.scale) - len(digits)
		if pad >= 0 {
			result = "0." + strings.Repeat("0", pad) + digits
		} else {
			split := len(digits) - int(d.scale)
			result = digits[:split] + "." + digits[split:]
		}
	case d.scale < 0:
		// Append (|scale|) trailing zeros.
		result = digits + strings.Repeat("0", int(-d.scale))
	}
	if neg {
		result = "-" + result
	}
	return result
}

// MarshalJSON encodes the Decimal as a JSON number (not string) using
// Canonical form.
func (d Decimal) MarshalJSON() ([]byte, error) {
	return []byte(d.Canonical()), nil
}

// UnmarshalJSON decodes a JSON number or string into the Decimal.
func (d *Decimal) UnmarshalJSON(data []byte) error {
	s := string(data)
	// Strip surrounding quotes if present (accept both number and string forms).
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	parsed, err := ParseDecimal(s)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}
