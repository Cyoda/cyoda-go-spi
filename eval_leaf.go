package spi

import (
	"fmt"
	"regexp"
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
//     other branches) plus a Void flag or an error. This is the once-per-query
//     work (§7).
//   - EvalLeaf classifies a single stored gjson.Result and decides match/no-match
//     against a pre-built Expansion.
//
// EvalLeafString is a convenience for single-entity callers that expands and
// evaluates in one call (with a result-identical fast path for the common
// monomorphic leaf).
//
// Divergences from Cloud, all intentional (see the brief and entity-search.md
// §9/§10):
//   - Null/absent uniformity: an absent-or-JSON-null stored leaf is a non-match
//     for EVERY binary op including the negatives (NE, INE, INOT_*). Negatives
//     are null-guarded to non-match, not implemented as !positive.
//   - BETWEEN uses precise same-type bounds (no double-widening quirk) and is
//     EXCLUSIVE (an inclusive variant is a trivial later addition — see
//     evalBetween).
//   - String ops act only on a textual stored value; a string op against a
//     non-textual (numeric/boolean) stored slot is a non-match and never
//     stringifies the stored value.
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
// opaque to callers — build it with ExpandLeaf and pass it to EvalLeaf. A Void
// expansion (>=1 declared type accepted the operand but every sub-condition was
// dropped) evaluates to non-match for any stored value.
type Expansion struct {
	kind expKind
	op   FilterOp
	void bool

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
	strRegex   *regexp.Regexp // compiled+anchored pattern for LIKE / MATCHES_PATTERN
}

// Void reports whether this expansion is the void (unsatisfiable-but-valid)
// leaf: the operand parsed as at least one declared type, but every candidate
// sub-condition was dropped (e.g. an imprecise EQUALS against integer-only
// buckets). A void leaf never matches. Exposed so a group combiner can treat it
// as OR-drop / AND-annihilate without re-deriving it.
func (e Expansion) Void() bool { return e.void }

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
func ExpandLeaf(op FilterOp, operand string, values []string, declared []DataType) (Expansion, error) {
	switch op {
	case FilterIsNull, FilterNotNull:
		return Expansion{kind: kindUnary, op: op}, nil

	case FilterContains, FilterStartsWith, FilterEndsWith, FilterLike, FilterMatchesRegex,
		FilterNotContains, FilterNotStartsWith, FilterNotEndsWith,
		FilterIEq, FilterINe, FilterIContains, FilterINotContains, FilterIStartsWith,
		FilterINotStartsWith, FilterIEndsWith, FilterINotEndsWith:
		e := Expansion{kind: kindStringOp, op: op, strOperand: operand}
		switch op {
		case FilterLike:
			if re, err := regexp.Compile(anchor(likeToRegex(operand))); err == nil {
				e.strRegex = re
			}
		case FilterMatchesRegex:
			if re, err := regexp.Compile(anchor(operand)); err == nil {
				e.strRegex = re
			}
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
			e.numeric = ExpandNumericOperand(dec, numericDeclared, op)
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
		return Expansion{}, fmt.Errorf("ExpandLeaf: operand %q parses into no declared type", operand)
	}
	if len(e.numeric) == 0 && len(e.temporal) == 0 && len(e.others) == 0 {
		// A type accepted the operand but every bucket dropped it → void.
		return Expansion{kind: kindCompare, op: op, void: true}, nil
	}
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

	if exp.void {
		return false
	}

	switch exp.kind {
	case kindStringOp:
		if stored.Type != gjson.String {
			return false // string op on a non-textual slot → non-match
		}
		return evalStringOp(exp, stored.String())
	case kindCompare:
		return exp.evalCompare(stored)
	case kindBetween:
		return exp.evalBetween(stored)
	}
	return false
}

// evalCompare runs the OR-over-branches comparison. Only the branch family that
// matches the stored value's own JSON kind participates, which is exactly the
// Cloud "a branch whose type-slot is absent is harmlessly false" behaviour.
func (e Expansion) evalCompare(stored gjson.Result) bool {
	switch stored.Type {
	case gjson.Number:
		dec, err := ParseDecimal(stored.Raw)
		if err != nil {
			return false
		}
		storedT := classifyStoredNumeric(dec)
		for _, sc := range e.numeric {
			if !IsAssignableTo(storedT, sc.Type) {
				continue
			}
			if sc.NotNull {
				return true // bare existence test: a present numeric of an assignable type
			}
			if cmpResult(dec.Cmp(sc.Value), sc.Op) {
				return true
			}
		}
		return false

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
					if CompareTemporal(tc.Op, storedMs, true, tc.Millis, 0, true) {
						return true
					}
				}
			}
		}
		for _, oc := range e.others {
			switch oc.typ {
			case String:
				if cmpResult(strings.Compare(s, oc.val.(string)), e.op) {
					return true
				}
			case Character:
				rs := []rune(s)
				if len(rs) == 1 && cmpResult(compareRune(rs[0], oc.val.(rune)), e.op) {
					return true
				}
			case UUIDType, TimeUUIDType:
				if id, err := uuid.Parse(s); err == nil {
					if eqNeResult(id == oc.val.(uuid.UUID), e.op) {
						return true
					}
				}
			}
		}
		return false

	case gjson.True, gjson.False:
		b := stored.Bool()
		for _, oc := range e.others {
			if oc.typ == Boolean && eqNeResult(b == oc.val.(bool), e.op) {
				return true
			}
		}
		return false
	}
	return false
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
		storedT := classifyStoredNumeric(dec)
		assignable := false
		for _, u := range e.numTypes {
			if IsAssignableTo(storedT, u) {
				assignable = true
				break
			}
		}
		if !assignable {
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
		return e.strRegex != nil && e.strRegex.MatchString(s)
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

// EvalLeafString expands and evaluates a leaf in one call, for single-entity
// callers. It carries a result-identical fast path for the common monomorphic
// leaf (see evalLeafFast); on any error from expansion it returns (false, err)
// for the caller to map to a 4xx.
func EvalLeafString(op FilterOp, operand string, values []string, declared []DataType, stored gjson.Result) (bool, error) {
	if matched, handled := evalLeafFast(op, operand, declared, stored); handled {
		return matched, nil
	}
	exp, err := ExpandLeaf(op, operand, values, declared)
	if err != nil {
		return false, err
	}
	return EvalLeaf(exp, stored), nil
}

// evalLeafFast is a strictly result-identical shortcut for the two most common
// monomorphic leaves, avoiding the full expansion + bucket machinery:
//
//   - a single declared STRING under a comparable op, and
//   - a single declared UNBOUND_DECIMAL under a comparable op (the universal
//     numeric sink: verbatim bounds, no rounding, every numeric stored value
//     assignable).
//
// Both reproduce the general path exactly. Bounded numeric types are
// deliberately NOT fast-pathed: their bucket range/rounding/void behaviour is
// the whole point and cannot be shortcut safely. handled=false means "not
// eligible — use the general path".
func evalLeafFast(op FilterOp, operand string, declared []DataType, stored gjson.Result) (matched, handled bool) {
	if len(declared) != 1 {
		return false, false
	}
	switch op {
	case FilterEq, FilterNe, FilterGt, FilterGte, FilterLt, FilterLte:
	default:
		return false, false
	}
	// Null/absent uniformity applies uniformly here too.
	nullish := !stored.Exists() || stored.Type == gjson.Null

	switch declared[0] {
	case String:
		if nullish {
			return false, true
		}
		if stored.Type != gjson.String {
			return false, true // monomorphic STRING vs non-textual stored → non-match
		}
		return cmpResult(strings.Compare(stored.String(), operand), op), true

	case UnboundDecimal:
		opDec, err := ParseDecimal(operand)
		if err != nil {
			return false, false // operand not numeric → let the general path raise the mismatch
		}
		if nullish {
			return false, true
		}
		if stored.Type != gjson.Number {
			return false, true
		}
		storedDec, err := ParseDecimal(stored.Raw)
		if err != nil {
			return false, true
		}
		return cmpResult(storedDec.Cmp(opDec), op), true
	}
	return false, false
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

// classifyStoredNumeric maps a stored numeric Decimal to its narrowest DataType,
// so the assignability gate can pick a sub-condition (e.g. stored 5 classifies as
// INTEGER and is assignable to a [LONG] sub-condition).
func classifyStoredNumeric(d Decimal) DataType {
	s := d.StripTrailingZeros()
	if s.Scale() <= 0 {
		if n, err := s.SetScale(0); err == nil {
			return ClassifyInteger(n.Unscaled())
		}
	}
	return ClassifyDecimal(s)
}

func fold(s string) string { return strings.ToLower(s) }

// --- LIKE grammar (Cloud queryable/Like.java prepareSpecialCharacters) -------

// regexpSpecialChars mirrors Cloud Like.REGEXP_SPECIAL_CHARS: every char here is
// escaped to a literal in the compiled pattern.
const regexpSpecialChars = "[](){}.*+?$^|#<>-="

// anchor wraps a regex body so it must match the WHOLE stored string, matching
// Java's Pattern.matcher(x).matches() semantics (Go's MatchString is otherwise
// an unanchored substring search).
func anchor(body string) string {
	return `\A(?:` + body + `)\z`
}

// likeToRegex ports Like.prepareSpecialCharacters: '%' → '.*?', '_' → '.', every
// regexp metacharacter escaped as a literal, '\' the escape char (\%, \_, \\ →
// literal %, _, \). Case-sensitive; the caller anchors the result.
func likeToRegex(s string) string {
	if s == "" {
		return ""
	}
	rs := []rune(s)
	sb := make([]rune, 0, len(rs)*2)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case strings.ContainsRune(regexpSpecialChars, c):
			sb = append(sb, '\\', c)
		case c == '_':
			if !hasEscapeRune(rs, i) {
				sb = append(sb, '.')
			} else {
				sb = sb[:len(sb)-1] // drop the raw '\' appended by the else-branch
				sb = append(sb, c)
			}
		case c == '%':
			if !hasEscapeRune(rs, i) {
				sb = append(sb, '.', '*', '?')
			} else {
				sb = sb[:len(sb)-1]
				sb = append(sb, c)
			}
		default:
			sb = append(sb, c)
		}
	}
	return string(sb)
}

// hasEscapeRune reports whether the rune at idx is escaped: an odd number of
// immediately-preceding backslashes (Cloud Like.hasEscapeCharacter).
func hasEscapeRune(rs []rune, idx int) bool {
	if idx == 0 {
		return false
	}
	count := 0
	for i := idx - 1; i >= 0 && rs[i] == '\\'; i-- {
		count++
	}
	return count%2 == 1
}
