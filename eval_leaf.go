package spi

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// eval_leaf.go is the type-directed leaf-comparison kernel: the single
// authoritative comparator for a search leaf against a stored JSON value. It
// assembles the numeric-bucket expansion (numeric_bucket.go), the temporal
// resolution graph (temporal_subtype.go / temporal.go) and the per-type operand
// parser (parse_typed.go) into one place, faithfully following the Cloud
// polymorphic search semantics (entity-search.md §6, §5) while making the
// deliberate cyoda-go divergences the design calls for.
//
// Two entry points:
//
//   - ExpandLeaf parses the operand once against the field's declared type set
//     and produces an Expansion — the typed sub-conditions (numeric / temporal /
//     other branches) — or an error. This is the once-per-query work, and
//     prepared_filter.go is what makes that true: Prepare calls it once per
//     leaf and Match never calls it at all.
//   - EvalLeaf classifies a single stored gjson.Result and decides match/no-match
//     against a pre-built Expansion. This is the per-row work.
//
// There is deliberately no fused expand-and-evaluate entry point. One existed,
// and it is why operand parsing, type bucketing and regex compilation ran once
// per candidate entity instead of once per query.
//
// Divergences from Cloud, all intentional (see the brief and entity-search.md
// §9/§10):
//   - Null/absent uniformity: an absent-or-JSON-null stored leaf is a non-match
//     for EVERY binary op including the negatives (NE, INE, INOT_*). Negatives
//     are null-guarded to non-match, not implemented as !positive.
//   - BETWEEN uses precise same-type bounds (no double-widening quirk) and is
//     EXCLUSIVE (an inclusive variant is a trivial later addition — see
//     evalBetween).
//   - String ops act only on a textual stored value; a string op never
//     stringifies the stored value, so a string op against a non-textual
//     (numeric/boolean) stored slot has no candidate to test — it follows
//     operator polarity (see isNegativeOp), not an unconditional non-match.
//   - BETWEEN_INCLUSIVE is BETWEEN's inclusive twin (lo <= v <= hi vs lo < v <
//     hi); it shares BETWEEN's expansion/bucketing exactly and differs only in
//     the final bound comparison (see rangeMatch in evalBetween).

// expKind discriminates the operator families an Expansion can hold.
type expKind int

const (
	kindUnary    expKind = iota // IS_NULL / NOT_NULL
	kindStringOp                // CONTAINS / STARTS_WITH / …/ LIKE / MATCHES / I*/INOT_*
	kindCompare                 // EQ / NE / GT / GTE / LT / LTE
	kindBetween                 // BETWEEN / BETWEEN_INCLUSIVE
)

// otherCond is one same-type sub-condition for a non-numeric, non-temporal
// declared type (String / Character / Boolean / UUID). val is the operand
// already parsed to that type by ParseStringOrNull.
type otherCond struct {
	typ DataType
	val any
}

// tempRange is one resolved temporal BETWEEN sub-condition: floored lo/hi
// epoch-millis for a single declared subtype. typ is that subtype — the stored
// value is matched against this range only when its own natural subtype equals
// typ (temporal subtypes are exact-match slots, not a widening lattice).
type tempRange struct {
	typ    DataType
	lo, hi int64
}

// Expansion is the once-per-query parse+bucket result of a single leaf. It is
// opaque to callers — build it with ExpandLeaf and pass it to EvalLeaf.
//
// A kindCompare expansion can have every numeric/temporal/other bucket empty
// (>=1 declared type accepted the operand, but every sub-condition it produced
// was then dropped — e.g. EQUALS against an imprecise value). That is not a
// distinct "void" case: at eval time the stored value's own type family
// simply has no candidate sub-condition, exactly like a declared type that
// never accepted the operand in the first place. EvalLeaf answers such an
// unsatisfiable comparison by operator polarity (isNegativeOp) — false for a
// positive operator, true for a negative one — not with an unconditional
// non-match; see evalCompare's hadCandidate contract.
type Expansion struct {
	kind expKind
	op   FilterOp

	// kindCompare branches (OR across families; only the family matching the
	// stored value's own JSON kind participates).
	numeric  []NumericSubCondition
	temporal []TemporalSubCondition
	others   []otherCond

	// kindBetween precise bounds.
	numTypes   []DataType
	numLo      Decimal
	numHi      Decimal
	numOK      bool
	tempRanges []tempRange
	strBetween bool
	strLo      string
	strHi      string

	// kindStringOp payload.
	strOperand string
	strMatch   patternMatcher // LIKE glob / MATCHES_PATTERN anchored regex; nil ⇒ never matches
}

// compileRegex is regexp.Compile behind a package var so an internal test can
// count compilations and prove they happen once per query rather than once per
// row. Production code never reassigns it.
var compileRegex = regexp.Compile

// maxEchoedOperandBytes bounds how much of a leaf's operand this file's error
// paths repeat back to the caller. Search request bodies are capped far
// larger than this (10 MiB), so echoing the operand verbatim would let a
// single oversized-but-otherwise-ordinary request (e.g. a field with no
// declared type — the documented 400 INVALID_CONDITION case, not a
// boundary/backend inconsistency) blow the error up to request size, and
// prepared_filter.go wraps this same operand a second time on top of it.
// internal/common's error path then logs that string again as "cause" at
// WARN, so an unbounded echo turns a client-triggerable, entirely ordinary
// rejection into tens of megabytes of log per request. Mirrors
// [ErrInvalidPattern]'s choice (see its doc comment) to drop the operand from
// a client-facing 400 entirely; this error still names it, just bounded.
const maxEchoedOperandBytes = 200

// truncateOperand caps s for inclusion in a client-facing error message. The
// "...(truncated)" marker is explicit so a reader — including one piecing the
// message back together from a log line — can tell truncation happened
// rather than mistaking the cut string for the operand in full.
func truncateOperand(s string) string {
	if len(s) <= maxEchoedOperandBytes {
		return s
	}
	return s[:maxEchoedOperandBytes] + "...(truncated)"
}

// ExpandLeaf parses operand (or, for range ops, the two bounds in values)
// against the field's declared type set and returns the typed Expansion.
//
// Input contract:
//   - Unary ops (IS_NULL / NOT_NULL): operand and values are ignored.
//   - Binary ops (the six comparables, all string ops): operand is the single
//     value; values is ignored.
//   - Range ops (BETWEEN / BETWEEN_INCLUSIVE): values must hold exactly the two
//     bounds; operand is ignored.
//
// Errors (the caller maps to INVALID_CONDITION / CONDITION_TYPE_MISMATCH):
//   - a range op whose values is not exactly length 2 → arity error;
//   - a compare op whose operand parses into NO declared type → type-mismatch.
//
// Note on shape errors the string inputs cannot express — a JSON-null operand on
// a binary/range op, or an object/array operand where a scalar is required — are
// detected by the caller, which holds the raw JSON value, before it reaches this
// string-typed boundary (entity-search.md §8).
//
// Kernel contract on declared: the caller is expected to pass AT MOST ONE
// numeric DataType in declared — cyoda-go's TypeSet collapses the numeric
// family down to its single narrowest bucket via CollapseNumeric before it
// ever reaches this function, and every production caller goes through
// that collapse. A declared set carrying more than one numeric type is
// outside what this kernel guarantees: a stored value can then satisfy
// several of those buckets rather than the single narrowest one an
// uncollapsed caller might expect, because admission (AdmitsNumeric) judges
// each declared type independently rather than picking one canonical
// bucket for the value.
func ExpandLeaf(op FilterOp, operand string, values []string, declared []DataType) (Expansion, error) {
	switch op {
	case FilterIsNull, FilterNotNull:
		return Expansion{kind: kindUnary, op: op}, nil

	case FilterContains, FilterStartsWith, FilterEndsWith, FilterLike, FilterMatchesRegex,
		FilterNotContains, FilterNotStartsWith, FilterNotEndsWith,
		FilterIEq, FilterINe, FilterIContains, FilterINotContains, FilterIStartsWith,
		FilterINotStartsWith, FilterIEndsWith, FilterINotEndsWith:
		e := Expansion{kind: kindStringOp, op: op, strOperand: operand}
		// Swallowed deliberately: ExpandLeaf's own per-row contract is
		// unchanged by prepared_filter.go's Prepare — a pattern operand that
		// will not compile still yields a leaf whose Expansion never matches
		// (nil strMatch here), for callers outside this package that invoke
		// ExpandLeaf directly and still want that leaf built rather than an
		// error.
		//
		// Prepare is no longer one of those callers: it now rejects such a
		// leaf (ErrUnevaluableLeaf). It gets there by checking
		// exp.strMatch == nil AFTER this call returns, not by asking
		// ValidateLeafPattern FIRST — asking first would run compileLeafPattern
		// twice per leaf (once in ValidateLeafPattern, once here) and break
		// the once-per-query compile guarantee
		// (TestPrepare_CompilesRegexExactlyOncePerQuery /
		// TestPrepare_TokenisesLikeExactlyOncePerQuery). That after-the-fact
		// check is sound because TestValidatorAgreesWithKernel pins that
		// compileLeafPattern erroring is exactly when strMatch ends up nil —
		// Prepare's post-hoc check cannot silently miss a case
		// ValidateLeafPattern would have caught.
		if m, err := compileLeafPattern(op, operand); err == nil {
			e.strMatch = m
		}
		return e, nil

	case FilterBetween, FilterBetweenInclusive:
		return expandBetween(op, values, declared)

	case FilterEq, FilterNe, FilterGt, FilterGte, FilterLt, FilterLte:
		return expandCompare(op, operand, declared)

	default:
		return Expansion{}, fmt.Errorf("ExpandLeaf: unsupported leaf operator %q", op)
	}
}

// bucketDeclared splits a declared type set into numeric / temporal / other.
func bucketDeclared(declared []DataType) (numeric, temporal, other []DataType) {
	for _, t := range declared {
		switch {
		case IsNumeric(t):
			numeric = append(numeric, t)
		case isTemporalSubtype(t):
			temporal = append(temporal, t)
		default:
			other = append(other, t)
		}
	}
	return
}

// expandCompare handles the six comparable operators: numeric-bucket expansion,
// temporal resolution, and per-type direct parse, unioned as OR branches.
func expandCompare(op FilterOp, operand string, declared []DataType) (Expansion, error) {
	numericDeclared, temporalDeclared, otherDeclared := bucketDeclared(declared)
	e := Expansion{kind: kindCompare, op: op}
	engaged := false

	if len(numericDeclared) > 0 {
		if dec, err := ParseDecimal(operand); err == nil {
			engaged = true // the operand IS a number → the numeric family is applicable
			// One literal, one meaning. Ingestion classifies by value, after
			// stripping trailing zeros; the operand must be read the same way
			// or "5.0" and "5" denote different things on the two sides.
			// Stripping here rather than inside foldToInt covers both
			// families: the decimal bucket computes precision on the operand
			// too, so "5.000000000000000000" would otherwise be judged
			// imprecise and have its EQUALS branch dropped.
			e.numeric = ExpandNumericOperand(dec.StripTrailingZeros(), numericDeclared, op)
		}
	}

	if len(temporalDeclared) > 0 {
		if _, ok := parseNatural(operand); ok {
			engaged = true // the operand IS a temporal → the temporal family is applicable
			e.temporal = ExpandTemporalOperand(operand, temporalDeclared, op)
		}
	}

	for _, t := range otherDeclared {
		if v, ok := ParseStringOrNull(operand, t); ok {
			engaged = true
			e.others = append(e.others, otherCond{typ: t, val: v})
		}
	}

	if !engaged {
		return Expansion{}, fmt.Errorf("ExpandLeaf: operand %q parses into no declared type", truncateOperand(operand))
	}
	// A declared type may have accepted the operand yet dropped every
	// sub-condition it produced (e.g. EQUALS against an imprecise value) —
	// e.numeric/e.temporal/e.others are then all empty, same as a type that
	// never accepted the operand. That is not a distinct case to construct
	// here: evalCompare/EvalLeaf already treat an empty family as "no
	// candidate" and answer by operator polarity (see Expansion's doc).
	return e, nil
}

// expandBetween builds a precise range expansion for BETWEEN and
// BETWEEN_INCLUSIVE alike — op only decides the bound inclusivity applied at
// eval time (evalBetween); the bucketing/parsing here is identical for both.
// Numeric bounds are compared with Decimal (no rounding, no double-widening);
// temporal bounds are resolved per declared subtype to floored epoch-millis; a
// declared String type enables lexicographic bounds.
func expandBetween(op FilterOp, values []string, declared []DataType) (Expansion, error) {
	if len(values) != 2 {
		return Expansion{}, fmt.Errorf("ExpandLeaf: range operator %q requires exactly 2 bounds, got %d", op, len(values))
	}
	numericDeclared, temporalDeclared, otherDeclared := bucketDeclared(declared)
	e := Expansion{kind: kindBetween, op: op}
	engaged := false

	if len(numericDeclared) > 0 {
		lo, errLo := ParseDecimal(values[0])
		hi, errHi := ParseDecimal(values[1])
		if errLo == nil && errHi == nil {
			engaged = true
			e.numOK = true
			e.numLo, e.numHi = lo, hi
			e.numTypes = numericDeclared
		}
	}

	if len(temporalDeclared) > 0 {
		_, okLo := parseNatural(values[0])
		_, okHi := parseNatural(values[1])
		if okLo && okHi {
			for _, t := range temporalDeclared {
				loMs, ok1 := resolveTemporalMillis(values[0], t)
				hiMs, ok2 := resolveTemporalMillis(values[1], t)
				if ok1 && ok2 {
					engaged = true
					e.tempRanges = append(e.tempRanges, tempRange{typ: t, lo: loMs, hi: hiMs})
				}
			}
		}
	}

	for _, t := range otherDeclared {
		if t == String {
			engaged = true
			e.strBetween = true
			e.strLo, e.strHi = values[0], values[1]
		}
	}

	if !engaged {
		return Expansion{}, fmt.Errorf("ExpandLeaf: range bounds parse into no declared type")
	}
	return e, nil
}

// resolveTemporalMillis floors operand to subtype t and returns its epoch-millis.
// It uses a comparing op (GT) purely so an imprecise downscale is never dropped
// (drops only happen for EQUALS); the returned millis is op-independent.
func resolveTemporalMillis(operand string, t DataType) (int64, bool) {
	src, ok := parseNatural(operand)
	if !ok {
		return 0, false
	}
	if src.Type == t {
		return src.Millis(), true
	}
	if cond, ok := convertTemporal(src.Type, t, src, FilterGt); ok {
		return cond.Millis, true
	}
	return 0, false
}

// EvalLeaf reports whether stored satisfies the pre-built leaf Expansion.
// exp was built by ExpandLeaf, so it inherits that function's declared
// contract: a multi-numeric declared set was outside the kernel's
// guarantees when exp was built, and nothing here re-checks that.
func EvalLeaf(exp Expansion, stored gjson.Result) bool {
	// Unary ops decide purely on presence — handle before any classification.
	if exp.kind == kindUnary {
		present := stored.Exists() && stored.Type != gjson.Null
		switch exp.op {
		case FilterIsNull:
			return !present
		case FilterNotNull:
			return present
		}
		return false
	}

	// Null/absent uniformity: non-match for every binary op, negatives included.
	if !stored.Exists() || stored.Type == gjson.Null {
		return false
	}

	switch exp.kind {
	case kindStringOp:
		if stored.Type != gjson.String {
			// A string op has no candidate against a non-textual stored slot:
			// it never stringifies the stored value, so there is nothing to
			// test. Follow operator polarity rather than answering false
			// unconditionally.
			return isNegativeOp(exp.op)
		}
		return evalStringOp(exp, stored.String())
	case kindCompare:
		matched, hadCandidate := exp.evalCompare(stored)
		if matched {
			return true
		}
		if !hadCandidate {
			return isNegativeOp(exp.op)
		}
		return false
	case kindBetween:
		return exp.evalBetween(stored)
	}
	return false
}

// isNegativeOp reports whether op asserts the ABSENCE of a relation. When no
// sub-condition survives for the stored value's own type family, the comparison
// is unsatisfiable for that value: a positive operator is false and a negative
// one is true. Null and absent are handled earlier and never reach here.
func isNegativeOp(op FilterOp) bool {
	switch op {
	case FilterNe, FilterINe,
		FilterNotContains, FilterINotContains,
		FilterNotStartsWith, FilterINotStartsWith,
		FilterNotEndsWith, FilterINotEndsWith:
		return true
	}
	return false
}

// evalCompare runs the OR-over-branches comparison. Only the branch family that
// matches the stored value's own JSON kind participates, which is exactly the
// Cloud "a branch whose type-slot is absent is harmlessly false" behaviour.
//
// The second return, hadCandidate, reports whether the comparison for the
// stored value's own type family is DECIDED — either a sub-condition was
// actually tried (matched or not), or the value could not be read at all and
// the family fails closed. EvalLeaf treats hadCandidate=false as the one case
// still open to interpretation: no sub-condition even existed to try for that
// family, so the answer follows operator polarity (isNegativeOp) rather than
// defaulting to false. hadCandidate=true always means the answer above
// (matched) is final and polarity plays no further part — that is what makes
// "value unreadable" and "sub-condition tried and failed" the same outcome:
// both report hadCandidate=true, matched=false, and EvalLeaf then answers
// false for every operator, negatives included. Per
// correctness-over-availability.md: a value the engine cannot read is a
// fail-closed non-match, never a substituted answer that a negative operator
// could flip to a match.
func (e Expansion) evalCompare(stored gjson.Result) (matched bool, hadCandidate bool) {
	switch stored.Type {
	case gjson.Number:
		dec, err := ParseDecimal(stored.Raw)
		if err != nil {
			// The value could not be read (e.g. an exponent Decimal cannot
			// represent) — NOT "no sub-condition existed to try". Fail closed
			// for every operator: hadCandidate=true pins the answer to
			// matched=false regardless of polarity, so a negative operator
			// never rides an unreadable value into the result set.
			return false, true
		}
		for _, sc := range e.numeric {
			// Judge the stored value against what the declared type admits,
			// not against the label the value happens to classify as. A
			// [DOUBLE] leaf holds 2147483648 — the label LONG does not widen
			// into DOUBLE, but the value is inside DOUBLE's range and well
			// within its mantissa, so the leaf holds it and search must find
			// it. AdmitsNumeric is the same predicate ingestion used to let
			// the value in.
			if !AdmitsNumeric(sc.Type, dec) {
				continue
			}
			hadCandidate = true
			if sc.NotNull {
				return true, true // bare existence test: a present numeric of an assignable type
			}
			if cmpResult(dec.Cmp(sc.Value), sc.Op) {
				return true, true
			}
		}
		return false, hadCandidate

	case gjson.String:
		s := stored.String()
		if len(e.temporal) > 0 {
			// Stored-side type-slot discipline: classify the stored ISO string to
			// its own natural subtype S (floored to epoch-millis), then compare it
			// only against the sub-condition declared for S. A coarse stored value
			// (LocalDate, Year, …) is offset-less and ParseTemporalMillis cannot
			// read it; and matching a ZonedDateTime instant against a YEAR branch
			// would be a spurious cross-subtype hit. Both are avoided by the
			// exact-subtype gate (temporal subtypes are not in the numeric widening
			// lattice, so this is equality, not IsAssignableTo).
			if src, ok := parseNatural(s); ok {
				storedMs := src.Millis()
				for _, tc := range e.temporal {
					if tc.Type != src.Type {
						continue
					}
					hadCandidate = true
					if CompareTemporal(tc.Op, storedMs, true, tc.Millis, true) {
						return true, true
					}
				}
			}
		}
		for _, oc := range e.others {
			switch oc.typ {
			case String:
				hadCandidate = true
				if cmpResult(strings.Compare(s, oc.val.(string)), e.op) {
					return true, true
				}
			case Character:
				rs := []rune(s)
				if len(rs) == 1 {
					hadCandidate = true
					if cmpResult(compareRune(rs[0], oc.val.(rune)), e.op) {
						return true, true
					}
				}
			case UUIDType, TimeUUIDType:
				if id, err := uuid.Parse(s); err == nil {
					hadCandidate = true
					if eqNeResult(id == oc.val.(uuid.UUID), e.op) {
						return true, true
					}
				}
			}
		}
		return false, hadCandidate

	case gjson.True, gjson.False:
		b := stored.Bool()
		for _, oc := range e.others {
			if oc.typ == Boolean {
				hadCandidate = true
				if eqNeResult(b == oc.val.(bool), e.op) {
					return true, true
				}
			}
		}
		return false, hadCandidate
	}
	return false, false
}

// evalBetween applies the precise range test, EXCLUSIVE for BETWEEN and
// INCLUSIVE for BETWEEN_INCLUSIVE — the two share every bucketing/parsing step
// (expandBetween) and differ only in the final bound comparison, done here via
// rangeMatch/rangeMatchMs.
func (e Expansion) evalBetween(stored gjson.Result) bool {
	inclusive := e.op == FilterBetweenInclusive
	switch stored.Type {
	case gjson.Number:
		if !e.numOK {
			return false
		}
		dec, err := ParseDecimal(stored.Raw)
		if err != nil {
			return false
		}
		// Same admission discipline as evalCompare, but over the whole
		// declared numeric set rather than per sub-condition: expandBetween
		// carries e.numTypes, not a sub-condition list. Under the collapse
		// invariant the set has at most one member and the two coincide.
		admitted := false
		for _, u := range e.numTypes {
			if AdmitsNumeric(u, dec) {
				admitted = true
				break
			}
		}
		if !admitted {
			return false
		}
		return rangeMatch(e.numLo.Cmp(dec), dec.Cmp(e.numHi), inclusive)

	case gjson.String:
		s := stored.String()
		if len(e.tempRanges) > 0 {
			// Same stored-side type-slot discipline as evalCompare: classify the
			// stored ISO string to its natural subtype and test it only against the
			// range declared for that exact subtype.
			if src, ok := parseNatural(s); ok {
				ms := src.Millis()
				for _, tr := range e.tempRanges {
					if tr.typ != src.Type {
						continue
					}
					if rangeMatchMs(tr.lo, ms, tr.hi, inclusive) {
						return true
					}
				}
			}
		}
		if e.strBetween && rangeMatch(strings.Compare(e.strLo, s), strings.Compare(s, e.strHi), inclusive) {
			return true
		}
		return false
	}
	return false
}

// rangeMatch decides a BETWEEN/BETWEEN_INCLUSIVE bound test from two three-way
// comparisons: loCmp is lo-vs-value (Cmp semantics: <0 means lo < value) and
// hiCmp is value-vs-hi (<0 means value < hi). inclusive=false requires both
// strict (<0); inclusive=true also accepts the on-the-bound case (<=0).
func rangeMatch(loCmp, hiCmp int, inclusive bool) bool {
	if inclusive {
		return loCmp <= 0 && hiCmp <= 0
	}
	return loCmp < 0 && hiCmp < 0
}

// rangeMatchMs is rangeMatch specialized for millisecond bounds, avoiding a
// three-way-comparison allocation on the temporal hot path.
func rangeMatchMs(lo, ms, hi int64, inclusive bool) bool {
	if inclusive {
		return lo <= ms && ms <= hi
	}
	return lo < ms && ms < hi
}

// evalStringOp applies a string operator to a textual stored value. The stored
// value is already known to be textual (EvalLeaf gate). Case-insensitive
// variants fold both sides with strings.ToLower; IEQUALS/INOT_EQUAL use
// strings.EqualFold (the closest unicode-aware analogue of equalsIgnoreCase).
func evalStringOp(e Expansion, s string) bool {
	op := e.strOperand
	switch e.op {
	case FilterContains:
		return strings.Contains(s, op)
	case FilterStartsWith:
		return strings.HasPrefix(s, op)
	case FilterEndsWith:
		return strings.HasSuffix(s, op)
	case FilterNotContains:
		return !strings.Contains(s, op)
	case FilterNotStartsWith:
		return !strings.HasPrefix(s, op)
	case FilterNotEndsWith:
		return !strings.HasSuffix(s, op)
	case FilterLike, FilterMatchesRegex:
		return e.strMatch != nil && e.strMatch.matches(s)
	case FilterIEq:
		return strings.EqualFold(s, op)
	case FilterINe:
		return !strings.EqualFold(s, op)
	case FilterIContains:
		return strings.Contains(fold(s), fold(op))
	case FilterINotContains:
		return !strings.Contains(fold(s), fold(op))
	case FilterIStartsWith:
		return strings.HasPrefix(fold(s), fold(op))
	case FilterINotStartsWith:
		return !strings.HasPrefix(fold(s), fold(op))
	case FilterIEndsWith:
		return strings.HasSuffix(fold(s), fold(op))
	case FilterINotEndsWith:
		return !strings.HasSuffix(fold(s), fold(op))
	}
	return false
}

// --- small comparison helpers ---------------------------------------------

// cmpResult maps a three-way comparison (-1/0/+1) onto an ordering/equality op.
func cmpResult(cmp int, op FilterOp) bool {
	switch op {
	case FilterEq:
		return cmp == 0
	case FilterNe:
		return cmp != 0
	case FilterGt:
		return cmp > 0
	case FilterGte:
		return cmp >= 0
	case FilterLt:
		return cmp < 0
	case FilterLte:
		return cmp <= 0
	}
	return false
}

// eqNeResult handles the equality-only types (Boolean, UUID): ordering ops are
// non-matches for them.
func eqNeResult(equal bool, op FilterOp) bool {
	switch op {
	case FilterEq:
		return equal
	case FilterNe:
		return !equal
	}
	return false
}

func compareRune(a, b rune) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func fold(s string) string { return strings.ToLower(s) }

// anchor wraps a regex body so it must match the WHOLE stored string, matching
// Java's Pattern.matcher(x).matches() semantics (Go's MatchString is otherwise
// an unanchored substring search).
//
// It is string concatenation, so it is only sound for a body that parses
// STANDALONE — see compileMatchesPattern.
func anchor(body string) string {
	return `\A(?:` + body + `)\z`
}

// --- pattern derivation ----------------------------------------------------

type regexMatcher struct{ re *regexp.Regexp }

func (m regexMatcher) matches(s string) bool { return m.re.MatchString(s) }

// invalidPatternError reports a regex failure by its syntax CODE only. It must
// never carry syntax.Error.Expr, which echoes the anchored expression and the
// caller's operand into a client-facing 400.
//
// It carries no operator name: naming the operator is the caller's job, in
// the vocabulary that caller's own caller speaks — [ValidateLeafPattern] names
// the FilterOp it was handed, [ValidateConditionPatterns] names the domain
// operator string the user wrote. Naming it here would fix it to neither.
func invalidPatternError(err error) error {
	var se *syntax.Error
	if errors.As(err, &se) {
		return fmt.Errorf("%w: %s", ErrInvalidPattern, se.Code)
	}
	return fmt.Errorf("%w: operand is not a valid regular expression", ErrInvalidPattern)
}

// compileMatchesPattern requires the operand to parse STANDALONE as well as
// compile ANCHORED.
//
// Anchoring is concatenation, so a body with a net-unmatched ')' escapes the
// group: ")|(" becomes \A(?:)|()\z, an alternation whose first branch \A(?:)
// matches the empty string at position 0 — it matches every stored value.
// Requiring a standalone parse makes that family unrepresentable, because RE2
// rejects an unmatched ')' on its own. The two accept-sets then agree in the
// safe direction: nothing is accepted that matches more than it says.
//
// The standalone check is syntax.Parse, NOT a second regexp.Compile.
// regexp.Compile is syntax.Parse plus program construction, so the parse alone
// rejects exactly the same operands — and building a second program we would
// throw away would make Prepare compile twice per query, breaking
// TestPrepare_CompilesRegexExactlyOncePerQuery.
//
// The standalone parse is also the honest diagnostic. For "[", it reports
// "missing closing ]"; the anchored form reports "invalid escape sequence"
// about a \z the caller never wrote.
func compileMatchesPattern(operand string) (patternMatcher, error) {
	if _, err := syntax.Parse(operand, syntax.Perl); err != nil {
		return nil, invalidPatternError(err)
	}
	re, err := compileRegex(anchor(operand))
	if err != nil {
		// The bare parse above already succeeded, so any syntax.Error.Code
		// here can only describe the \A(?:...)\z wrapper this function
		// added — never the operand the caller wrote. Reporting it would
		// point the caller at punctuation they never typed (e.g. a "missing
		// closing )" about anchor's own "(?:"). Report the honest, generic
		// fact instead: not usable as a whole-string pattern.
		return nil, fmt.Errorf("%w: operand is not usable as a whole-string pattern", ErrInvalidPattern)
	}
	return regexMatcher{re: re}, nil
}

// compileLeafPattern is the SINGLE derivation of what a pattern operand means.
// Both the kernel (via ExpandLeaf) and validators (via ValidateLeafPattern)
// route through it, so a validator cannot accept what the kernel refuses.
//
// It takes `any` and applies OperandString itself: a caller cannot supply a
// differently-derived operand because it does not derive one.
//
// A nil matcher with a nil error means "this operator compiles no pattern".
// Both nils must be UNTYPED — a typed-nil matcher through the interface is
// non-nil and evalStringOp would call a method on it.
func compileLeafPattern(op FilterOp, value any) (patternMatcher, error) {
	switch op {
	case FilterLike:
		return parseLikePattern(OperandString(value))
	case FilterMatchesRegex:
		return compileMatchesPattern(OperandString(value))
	}
	return nil, nil
}

// ValidateLeafPattern reports whether value is usable as op's pattern operand,
// using the SAME derivation the kernel evaluates with. A validator calling this
// cannot accept an operand the kernel will refuse, or refuse one it accepts.
//
// Returns nil for every operator that carries no pattern, so a caller can pass
// any leaf without switching on the operator first.
//
// It covers pattern VALIDITY only. Passing it is not the same as having
// validated the condition — see [ValidateConditionOperators] for operator
// names. Errors wrap [ErrInvalidPattern], name op (the exact FilterOp the
// caller passed in — accurate here, since the caller supplied it), and carry
// neither the operand nor the anchored form.
func ValidateLeafPattern(op FilterOp, value any) error {
	if _, err := compileLeafPattern(op, value); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return nil
}
