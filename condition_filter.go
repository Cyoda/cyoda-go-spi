package spi

import (
	"fmt"
	"strings"

	"github.com/cyoda-platform/cyoda-go-spi/predicate"
)

// ConditionToFilter translates a [predicate.Condition] into a [Filter]. It is
// the anti-corruption layer between the domain's predicate syntax and the
// stable filter contract storage plugins use for pushdown, and it is the only
// supported way to produce a Filter that the leaf-comparison kernel
// ([MatchFilter] / [EvalLeaf]) will evaluate correctly.
//
// It lives in the SPI, not in the engine, because a backend that
// self-executes a search — one that receives a serialized condition rather
// than a ready-made Filter, e.g. an async search job it runs itself — has no
// other way to reach the kernel. Without it such a backend must ship a second
// evaluator, which then drifts from this one and answers the same query
// differently.
//
// # fields, and why nil is not a safe default
//
// fields is the model's flattened field view (JSONPath → [FieldDescriptor]),
// normally obtained via [FieldsMapFromSchema] over
// [ModelDescriptor.Schema]. It supplies each data leaf's declared types,
// which the kernel dispatches on.
//
// A nil or incomplete fields map does not error, and the result is worse than
// either "correct" or "empty": it is INTERNALLY INCONSISTENT. An empty
// declared set does not degrade every leaf the same way, because the kernel
// only consults declared types for the leaves that need a type slot to
// compare in:
//
//   - The EIGHT COMPARISON AND ORDERING leaves ANNIHILATE to false: EQUALS,
//     NOT_EQUAL, GREATER_THAN, GREATER_OR_EQUAL, LESS_THAN, LESS_OR_EQUAL,
//     BETWEEN, BETWEEN_INCLUSIVE. ExpandLeaf engages no type bucket, errors,
//     and evalLeafFilter swallows that into a non-match.
//   - The OTHER EIGHTEEN evaluate NORMALLY, because they never needed a
//     declared type. Presence: IS_NULL, NOT_NULL — decided purely from
//     whether the stored value is present and non-null (see ExpandLeaf's
//     kindUnary arm, which returns before declared is read), so they are NOT
//     comparisons despite the null operand. String and pattern: CONTAINS,
//     NOT_CONTAINS, STARTS_WITH, NOT_STARTS_WITH, ENDS_WITH, NOT_ENDS_WITH,
//     LIKE, MATCHES_PATTERN, and the case-insensitive family IEQUALS,
//     INOT_EQUAL, ICONTAINS, INOT_CONTAINS, ISTARTS_WITH, INOT_STARTS_WITH,
//     IENDS_WITH, INOT_ENDS_WITH — all handled by ExpandLeaf's kindStringOp
//     arm, which compares stringified forms and never reads declared.
//
// The negated and case-insensitive string operators are easy to overlook
// here: ICONTAINS resembles a comparison but is not one, and it keeps
// evaluating against a nil declared set exactly as CONTAINS does.
//
// So a condition mixing the two kinds yields wrong answers in a
// structure-dependent direction, not merely fewer: under AND a dropped
// comparison conjunct removes rows that should have matched, while under OR a
// surviving string disjunct admits rows the failed comparison was supposed to
// exclude. Both are silent.
//
// This is strictly more dangerous than uniformly returning nothing, which at
// least looks like an anomaly. Callers that cannot supply declared types
// should treat that as an error and refuse the query, rather than proceeding
// with a filter that is part-evaluated and part-annihilated.
//
// Meta leaves are unaffected: their types come from the static meta
// vocabulary, not from fields, so a nil map does not degrade them at all.
//
// # An unrecognised operator is an error, not a fallback
//
// A leaf whose OperatorType is outside the closed set [OperatorNames] reports
// fails with [ErrUnknownOperator]. Callers should map that to a client error
// (400 INVALID_CONDITION); it means the input was invalid, unlike the other
// failures here, which mean a well-formed predicate is not expressible as a
// pushdown filter.
//
// This is worth stating because the obvious alternative is actively harmful.
// Mapping an unrecognised name onto a real operator does not make it
// unevaluable — the kernel evaluates whatever it is given — so it silently
// answers a DIFFERENT question. Routing to a pattern match is the worst
// choice available: "NOT_EQUALS", the obvious misspelling of NOT_EQUAL,
// becomes an anchored regex that behaves as EQUALS and returns exactly the
// rows the caller meant to exclude.
//
// # Three obligations that remain the caller's
//
// ConditionToFilter validates operator names and path shape. It does not
// validate operands, and each of these fails SILENTLY — an under- or
// wrongly-populated result set, never an error:
//
//   - OBJECT OPERANDS. A leaf value that is an object denotes no scalar any
//     operator could compare against. Left unchecked it reaches the kernel,
//     which stringifies it via fmt.Sprint and compares the literal text
//     "map[a:1]". Reject a map-typed operand outright.
//   - BETWEEN ARITY. BETWEEN / BETWEEN_INCLUSIVE require exactly a two-element
//     [lo, hi] operand. Anything else leaves Filter.Values nil, ExpandLeaf
//     errors, and the leaf silently no-matches. Check the arity before
//     translating rather than diagnosing an empty result set afterwards.
//   - PATTERN COMPILABILITY. An uncompilable MATCHES_PATTERN operand leaves
//     the compiled program nil and the leaf silently returns false. Note the
//     kernel compiles the ANCHORED form while a naive caller-side check would
//     compile the raw operand, so the two accept sets are not identical.
func ConditionToFilter(cond predicate.Condition, fields map[string]FieldDescriptor) (Filter, error) {
	if cond == nil {
		return Filter{}, fmt.Errorf("condition is nil")
	}

	switch c := cond.(type) {
	case *predicate.SimpleCondition:
		return simpleToFilter(c, fields)
	case *predicate.LifecycleCondition:
		return lifecycleToFilter(c)
	case *predicate.GroupCondition:
		return groupToFilter(c, fields)
	case *predicate.ArrayCondition:
		return arrayToFilter(c, fields)
	case *predicate.FunctionCondition:
		return Filter{}, fmt.Errorf("function conditions are not translatable to filters")
	default:
		return Filter{}, fmt.Errorf("unsupported condition type: %T", cond)
	}
}

// simpleToFilter translates a SimpleCondition to a Filter with SourceData.
// Returns an error if the path cannot be represented as a pushdown filter.
func simpleToFilter(c *predicate.SimpleCondition, fields map[string]FieldDescriptor) (Filter, error) {
	stripped, err := stripDollarDot(c.JsonPath)
	if err != nil {
		return Filter{}, err
	}
	op, ok := LookupOperator(c.OperatorType)
	if !ok {
		return Filter{}, unknownOperatorError(c.OperatorType)
	}
	// FieldsMap keys are always "$."-prefixed. A condition's jsonPath may
	// legitimately omit the prefix, and callers' path validation normalises
	// before checking, so an unprefixed path reaches here as a known field.
	// Look it up the same way, or it misses the map, Declared comes back empty,
	// and the type-directed kernel expands a comparison leaf with no declared
	// type into nothing — a field that exists and holds matching data answers
	// with an empty page. arrayToFilter normalises via arrayElementPath; this
	// arm must too.
	key := NormalisePath(c.JsonPath)
	return Filter{
		Op:       op,
		Path:     stripped,
		Source:   SourceData,
		Value:    c.Value,
		Values:   betweenValues(op, c.Value),
		Coercion: dataCoercion(key, fields),
		Declared: fields[key].Types,
	}, nil
}

// unknownOperatorError reports an operatorType outside the closed set,
// listing the valid names so a caller can self-correct.
func unknownOperatorError(op string) error {
	if op == "" {
		return fmt.Errorf("%w: missing operatorType; valid: %s",
			ErrUnknownOperator, strings.Join(canonicalOperatorNames, ", "))
	}
	return fmt.Errorf("%w: %q; valid: %s",
		ErrUnknownOperator, op, strings.Join(canonicalOperatorNames, ", "))
}

// betweenValues returns the two BETWEEN / BETWEEN_INCLUSIVE bounds as a []any
// for consumers that read Filter.Values (the kernel's range evaluation, and
// the SQL backends' query planners). Every range consumer reads Values, not
// Value — leaving Values unset makes the range op silently never match.
// Returns nil for non-range ops or a malformed (non 2-element []any) value;
// validation elsewhere rejects malformed range conditions, and a nil Values
// correctly no-matches downstream rather than panicking.
func betweenValues(op FilterOp, value any) []any {
	if op != FilterBetween && op != FilterBetweenInclusive {
		return nil
	}
	vals, ok := value.([]any)
	if !ok || len(vals) != 2 {
		return nil
	}
	return vals
}

// dataCoercion returns CoerceTemporal only if the schema classifies the
// field's declared type(s) as temporal. A data field discovered as a temporal
// subtype (content-sniffed ISO-8601 sample values) classifies as
// [OrderTemporal] via [ClassifyType], so this stamps CoerceTemporal and routes
// the temporal pushdown path for it. A nil fields map yields CoerceNone.
func dataCoercion(jsonPath string, fields map[string]FieldDescriptor) FilterCoercion {
	if fields == nil {
		return CoerceNone
	}
	fd, ok := fields[jsonPath]
	if !ok {
		return CoerceNone
	}
	if kind, err := ClassifyType(fd.Types); err == nil && kind == OrderTemporal {
		return CoerceTemporal
	}
	return CoerceNone
}

// lifecycleToFilter translates a LifecycleCondition to a Filter with
// SourceMeta. The "previousTransition" alias is canonicalized to its
// storage-vocabulary name "transitionForLatestSave" (see sortableMetaFields —
// the single source of truth for the meta vocabulary).
//
// Coercion is stamped CoerceTemporal for meta fields the vocabulary marks
// [OrderTemporal] (currently creationDate, lastUpdateTime). Declared is
// stamped from the same routing since meta fields have fixed types and are
// NOT drawn from the model fields map: temporal meta leaves declare
// [ZonedDateTime], every other meta leaf declares [String]. This is why meta
// filters keep working with a nil fields map while data filters do not.
func lifecycleToFilter(c *predicate.LifecycleCondition) (Filter, error) {
	field := c.Field
	if field == "previousTransition" {
		field = "transitionForLatestSave"
	}
	co := CoerceNone
	declared := []DataType{String}
	if IsTemporalMetaField(field) {
		co = CoerceTemporal
		declared = []DataType{ZonedDateTime}
	}
	op, ok := LookupOperator(c.OperatorType)
	if !ok {
		return Filter{}, unknownOperatorError(c.OperatorType)
	}
	return Filter{
		Op:       op,
		Path:     field,
		Source:   SourceMeta,
		Value:    c.Value,
		Values:   betweenValues(op, c.Value),
		Coercion: co,
		Declared: declared,
	}, nil
}

// groupToFilter translates a GroupCondition to a Filter with AND/OR children.
func groupToFilter(c *predicate.GroupCondition, fields map[string]FieldDescriptor) (Filter, error) {
	op := FilterAnd
	if strings.EqualFold(c.Operator, "OR") {
		op = FilterOr
	}
	children := make([]Filter, 0, len(c.Conditions))
	for _, child := range c.Conditions {
		f, err := ConditionToFilter(child, fields)
		if err != nil {
			return Filter{}, err
		}
		children = append(children, f)
	}
	return Filter{Op: op, Children: children}, nil
}

// arrayToFilter translates an ArrayCondition into an AND group of positional
// equality checks. Each non-nil value in the array becomes an equality filter
// on the corresponding array index (e.g. "tags.0", "tags.2"). Nil entries mean
// "skip this position". This makes individual checks pushable to SQL via
// json_extract and correctly evaluable in post-filtering.
//
// Declared is stamped on every positional leaf from the array ELEMENT's fields
// entry — recorded under the base path with a trailing "[*]" (see
// arrayElementPath) — when resolvable. An unresolvable element path leaves
// Declared nil on those leaves, and per the kernel's type-directed contract an
// empty declared set is a non-match for comparison operators.
func arrayToFilter(c *predicate.ArrayCondition, fields map[string]FieldDescriptor) (Filter, error) {
	basePath, err := stripDollarDot(c.JsonPath)
	if err != nil {
		return Filter{}, err
	}
	declared := fields[arrayElementPath(c.JsonPath)].Types
	var children []Filter
	for i, val := range c.Values {
		if val == nil {
			continue
		}
		children = append(children, Filter{
			Op:       FilterEq,
			Path:     fmt.Sprintf("%s.%d", basePath, i),
			Source:   SourceData,
			Value:    val,
			Declared: declared,
		})
	}
	if len(children) == 0 {
		// All positions are nil (don't-care) — matches everything.
		// Return a tautology: an empty AND is true.
		return Filter{Op: FilterAnd}, nil
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return Filter{Op: FilterAnd, Children: children}, nil
}

// arrayElementPath returns the fields-map key that addresses an
// ArrayCondition's element type. The model tree records an array leaf under
// its container path with a trailing "[*]" (so an ArrayCondition naming
// "$.tags" addresses the element type recorded at "$.tags[*]"). This ensures
// both the "$." prefix and the "[*]" suffix, tolerating callers that already
// supply either.
func arrayElementPath(rawPath string) string {
	p := NormalisePath(rawPath)
	if strings.HasSuffix(p, "[*]") {
		return p
	}
	return p + "[*]"
}

// NormalisePath returns raw in the "$."-prefixed convention, idempotently.
//
// It is exported because the "$."-prefixed form is the fields-map key
// convention: [FieldsMapFromSchema] emits keys in it, and a lookup that
// misses returns a zero [FieldDescriptor] with no declared types, which
// annihilates comparison leaves rather than erroring. A caller assembling
// fields-map keys must produce the same form this function does, so it is
// published rather than reimplemented per plugin.
func NormalisePath(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return p
	}
	if strings.HasPrefix(p, "$.") {
		return p
	}
	if strings.HasPrefix(p, "$") {
		return p
	}
	return "$." + p
}

// stripDollarDot removes the leading "$." from a JSONPath expression and
// validates that the resulting path does not contain array-wildcard or
// advanced JSONPath syntax that cannot be pushed down to storage backends.
// Returns ("", error) when the path contains characters outside the safe
// dotted-identifier subset (letters, digits, underscore, hyphen, and dots).
// Callers fall back to in-memory filtering when this returns an error.
func stripDollarDot(path string) (string, error) {
	stripped := path
	if len(path) > 2 && path[:2] == "$." {
		stripped = path[2:]
	}
	// Reject paths containing JSONPath wildcard/array-subscript syntax
	// (e.g. "[*]", "[0]"). Such paths require in-memory evaluation and cannot
	// be translated to pushdown filters.
	for _, c := range stripped {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_', c == '-', c == '.':
			// safe
		default:
			return "", fmt.Errorf("path %q contains non-pushdownable syntax (character %q)", path, c)
		}
	}
	return stripped, nil
}

// MaxConditionDepth caps recursion in [ValidateConditionOperators] to defend
// against stack exhaustion from a deeply nested predicate tree. Client-facing
// parsers cap incoming requests at a smaller depth, but a programmatically
// constructed tree bypasses that and can otherwise nest arbitrarily. 256 is
// well above any realistic query and well below the stack-blow threshold.
const MaxConditionDepth = 256

// LookupOperator translates a domain operator string to a [FilterOp],
// reporting whether the name was recognised.
//
// Use it to validate an operator name ahead of translation when you want to
// reject the whole request with your own diagnostic. It is not a safety
// requirement: [ConditionToFilter] rejects an unrecognised operator on its
// own.
func LookupOperator(op string) (FilterOp, bool) {
	f := MapOperator(op)
	return f, f != ""
}

// canonicalOperatorNames is the closed set of operator names [MapOperator]
// recognises, held separately because a Go type switch cannot be enumerated.
// TestOperatorNames_MatchesMapOperator pins the two against each other.
// Byte-order sorted (note ISTARTS_WITH precedes IS_NULL: '_' > 'T').
var canonicalOperatorNames = []string{
	"BETWEEN", "BETWEEN_INCLUSIVE", "CONTAINS", "ENDS_WITH",
	"EQUALS", "GREATER_OR_EQUAL", "GREATER_THAN", "ICONTAINS",
	"IENDS_WITH", "IEQUALS", "INOT_CONTAINS", "INOT_ENDS_WITH",
	"INOT_EQUAL", "INOT_STARTS_WITH", "ISTARTS_WITH", "IS_NULL",
	"LESS_OR_EQUAL", "LESS_THAN", "LIKE", "MATCHES_PATTERN",
	"NOT_CONTAINS", "NOT_ENDS_WITH", "NOT_EQUAL", "NOT_NULL",
	"NOT_STARTS_WITH", "STARTS_WITH",
}

// OperatorNames returns the sorted set of operator names [MapOperator]
// recognises, as a fresh slice the caller may retain or mutate.
//
// It exists so a caller can render a "valid operators are…" diagnostic, or
// validate membership, without maintaining a second copy of the table — a
// copy that would drift silently, since nothing would compare the two.
func OperatorNames() []string {
	out := make([]string, len(canonicalOperatorNames))
	copy(out, canonicalOperatorNames)
	return out
}

// ValidateConditionOperators walks a condition tree and returns an error
// naming the first unrecognised operator it finds, wrapping
// [ErrUnknownOperator]. The error text lists the canonical set so a caller can
// self-correct.
//
// It is a convenience, not a safety requirement: [ConditionToFilter] rejects
// an unrecognised operator on its own. Use this to reject the whole request up
// front, before any partial work, and to report the problem at the request
// boundary rather than mid-translation. It exists so that a backend wanting
// that does not write the recursion over the condition types itself — a second
// implementation surface of the kind relocating [ConditionToFilter] here was
// meant to remove.
//
// It covers ONLY operator names. The three operand obligations documented on
// [ConditionToFilter] are deliberately not folded in: two are cheap local
// checks a caller can apply while walking its own input, and the third depends
// on a pattern-cost bound this module has not settled. Passing this function
// is not the same as having validated the condition.
func ValidateConditionOperators(cond predicate.Condition) error {
	return validateOperatorsAtDepth(cond, 0)
}

func validateOperatorsAtDepth(cond predicate.Condition, depth int) error {
	if cond == nil {
		return nil
	}
	if depth >= MaxConditionDepth {
		return fmt.Errorf("condition depth exceeded (max %d)", MaxConditionDepth)
	}
	switch c := cond.(type) {
	case *predicate.SimpleCondition:
		return checkOperator(c.OperatorType)
	case *predicate.LifecycleCondition:
		return checkOperator(c.OperatorType)
	case *predicate.GroupCondition:
		for _, child := range c.Conditions {
			if err := validateOperatorsAtDepth(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	case *predicate.ArrayCondition:
		// Carries no operator — each positional value becomes an equality
		// leaf in arrayToFilter. Nothing to check.
		return nil
	default:
		// Including FunctionCondition, which carries no operator either.
		// ConditionToFilter rejects it on its own terms.
		return nil
	}
}

func checkOperator(op string) error {
	if _, ok := LookupOperator(op); !ok {
		return unknownOperatorError(op)
	}
	return nil
}

// MapOperator translates a domain operator string to a [FilterOp], returning
// the zero FilterOp for a name outside the closed set [OperatorNames] reports.
//
// It is exported because a caller may need to map operators independently of
// translation — the engine's condition type-soundness validator does — and
// should not keep a second copy of this table. Use [LookupOperator] where the
// recognised/unrecognised distinction matters.
//
// The zero FilterOp is not a valid leaf operator: [ExpandLeaf] rejects it and
// [ConditionToFilter] refuses to build a filter around it. An unrecognised
// name therefore cannot become an evaluable predicate by accident.
func MapOperator(op string) FilterOp {
	switch op {
	case "EQUALS":
		return FilterEq
	case "NOT_EQUAL":
		return FilterNe
	case "GREATER_THAN":
		return FilterGt
	case "LESS_THAN":
		return FilterLt
	case "GREATER_OR_EQUAL":
		return FilterGte
	case "LESS_OR_EQUAL":
		return FilterLte
	case "CONTAINS":
		return FilterContains
	case "STARTS_WITH":
		return FilterStartsWith
	case "ENDS_WITH":
		return FilterEndsWith
	case "LIKE":
		return FilterLike
	case "IS_NULL":
		return FilterIsNull
	case "NOT_NULL":
		return FilterNotNull
	case "BETWEEN":
		return FilterBetween
	case "BETWEEN_INCLUSIVE":
		return FilterBetweenInclusive
	case "MATCHES_PATTERN":
		return FilterMatchesRegex
	case "IEQUALS":
		return FilterIEq
	case "INOT_EQUAL":
		return FilterINe
	case "ICONTAINS":
		return FilterIContains
	case "INOT_CONTAINS":
		return FilterINotContains
	case "NOT_CONTAINS":
		return FilterNotContains
	case "ISTARTS_WITH":
		return FilterIStartsWith
	case "INOT_STARTS_WITH":
		return FilterINotStartsWith
	case "NOT_STARTS_WITH":
		return FilterNotStartsWith
	case "IENDS_WITH":
		return FilterIEndsWith
	case "INOT_ENDS_WITH":
		return FilterINotEndsWith
	case "NOT_ENDS_WITH":
		return FilterNotEndsWith
	default:
		return ""
	}
}
