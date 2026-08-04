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
//   - COMPARISON AND ORDERING leaves ANNIHILATE to false: EQUALS, NOT_EQUAL,
//     GREATER_THAN, GREATER_OR_EQUAL, LESS_THAN, LESS_OR_EQUAL, BETWEEN,
//     BETWEEN_INCLUSIVE. ExpandLeaf engages no type bucket, errors, and
//     evalLeafFilter swallows that into a non-match.
//   - PRESENCE and STRING/PATTERN leaves evaluate NORMALLY, because they
//     never needed a declared type: IS_NULL, NOT_NULL, CONTAINS,
//     STARTS_WITH, ENDS_WITH, LIKE, MATCHES_PATTERN. IS_NULL and NOT_NULL
//     are decided purely from whether the stored value is present and
//     non-null (see ExpandLeaf's kindUnary arm, which returns before
//     declared is read) — they are NOT comparisons despite the null operand.
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
// # Callers MUST validate operator names first
//
// ConditionToFilter does not reject an unrecognised OperatorType. It routes
// through [MapOperator], whose fallback is FilterMatchesRegex, so a
// misspelled or hostile operator produces a filter that translates without
// error and then EVALUATES — with the operand treated as a regular
// expression. `NOT_EQUALS` (a misspelling of NOT_EQUAL) inverts the intended
// polarity; an operand of ".*" matches every row.
//
// The engine is safe from this only because it runs a condition validator
// before translating. A caller that reaches ConditionToFilter directly —
// which is precisely the self-executing backend this function exists for —
// has no such gate. Validate operator names with [LookupOperator] and reject
// anything it reports as unknown BEFORE calling this function.
func ConditionToFilter(cond predicate.Condition, fields map[string]FieldDescriptor) (Filter, error) {
	if cond == nil {
		return Filter{}, fmt.Errorf("condition is nil")
	}

	switch c := cond.(type) {
	case *predicate.SimpleCondition:
		return simpleToFilter(c, fields)
	case *predicate.LifecycleCondition:
		return lifecycleToFilter(c), nil
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
	op := MapOperator(c.OperatorType)
	return Filter{
		Op:       op,
		Path:     stripped,
		Source:   SourceData,
		Value:    c.Value,
		Values:   betweenValues(op, c.Value),
		Coercion: dataCoercion(c.JsonPath, fields),
		Declared: fields[c.JsonPath].Types,
	}, nil
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
func lifecycleToFilter(c *predicate.LifecycleCondition) Filter {
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
	op := MapOperator(c.OperatorType)
	return Filter{
		Op:       op,
		Path:     field,
		Source:   SourceMeta,
		Value:    c.Value,
		Values:   betweenValues(op, c.Value),
		Coercion: co,
		Declared: declared,
	}
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
	p := normalisePath(rawPath)
	if strings.HasSuffix(p, "[*]") {
		return p
	}
	return p + "[*]"
}

// normalisePath returns raw in the "$."-prefixed convention, idempotently.
func normalisePath(raw string) string {
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

// LookupOperator translates a domain operator string to a [FilterOp],
// reporting whether the name was recognised.
//
// This is the form callers should use. [MapOperator] silently maps an unknown
// name to FilterMatchesRegex, which is safe only behind a validator; this one
// lets the caller reject the name instead. Validating with LookupOperator
// before calling [ConditionToFilter] is the documented caller obligation.
func LookupOperator(op string) (FilterOp, bool) {
	f := MapOperator(op)
	if f == FilterMatchesRegex && op != "MATCHES_PATTERN" {
		return f, false
	}
	return f, true
}

// MapOperator translates a domain operator string to a [FilterOp].
//
// It is exported because the engine's condition type-soundness validator maps
// operators independently of translation and must not keep a second copy of
// this table. Prefer [LookupOperator] unless you specifically want the
// unknown-operator fallback.
//
// # The unknown-operator fallback is a trap for direct callers
//
// An unrecognised operator maps to FilterMatchesRegex. In the engine that is
// safe by construction: a MATCHES_PATTERN leaf is not pushed down, so it
// degrades to post-filtering. There is no such stage here. A caller that maps
// an unknown operator with this function and then EVALUATES the result gets
// the operand interpreted as a regular expression — "Alice" then behaves like
// equality, and ".*" matches every row.
//
// Direct callers must therefore validate the operator string before mapping
// it, or treat a FilterMatchesRegex result for an operator that was not
// literally "MATCHES_PATTERN" as an error. The fallback exists to force
// post-filtering, not to make arbitrary input evaluable.
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
		return FilterMatchesRegex // forces post-filter for unknown ops
	}
}
