package spi

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cyoda-platform/cyoda-go-spi/predicate"
)

// ConditionToFilter translates a [predicate.Condition] into a [Filter]. It is
// the anti-corruption layer between the domain's predicate syntax and the
// stable filter contract storage plugins use for pushdown, and it is the only
// supported way to produce a Filter that the leaf-comparison kernel
// ([Prepare] / [EvalLeaf]) will evaluate correctly.
//
// It lives in the SPI, not in the engine, because a backend that
// self-executes a search — one that receives a serialized condition rather
// than a ready-made Filter, e.g. an async search job it runs itself — has no
// other way to reach the kernel. Without it such a backend must ship a second
// evaluator, which then drifts from this one and answers the same query
// differently.
//
// # jsonPath must be JSON Path nomenclature
//
// A condition's jsonPath is the WIRE form and is JSON Path syntax, so the
// "$." leader is REQUIRED:
//
//	jsonPath  = "$." segment ( "." segment )*
//	segment   = name subscript*
//	name      = 1*( ALPHA / DIGIT / "_" / "-" )   ; ASCII only
//	subscript = "[" ( "*" / 1*DIGIT ) "]"
//
// "$.amount" and "$.address.city" are paths. A bare "amount" is not one and
// is REJECTED with an error wrapping [ErrInvalidFilterPath] — it is not a
// tolerated alias. So are an empty path, an empty or trailing segment
// ("$..a", "$.a."), bracket-quoted property access ("$['x']", "$.['x']",
// `$.a["b"]`), a bracket spelling outside the two supported subscript forms
// ("$.a[", "$.a]", "$.a[-1]", "$.a[0:2]", "$.a[0,1]", "$.a[?(@.x)]"), and any
// character outside the segment set — including one that FOLLOWS a well-formed
// subscript ("$.a[0];DROP"). Callers should surface all of these as a client
// error (400), not as a reason to fall back.
//
// Distinguish this from the PLUGIN-FACING form: [Filter.Path] is what this
// function emits, and it is BARE ("amount"), with the leader already stripped.
// A "$."-prefixed Filter.Path is malformed.
//
// Metadata is not addressed through jsonPath at all: a
// [predicate.LifecycleCondition] names a member of the closed meta vocabulary
// ([MetaFieldNames]) directly and is not subject to this grammar. A data path
// that happens to spell "$._meta.state" is an ordinary dotted path and is
// accepted as one.
//
// A WELL-FORMED array-subscripted path ("$.tags[*].name", "$.arr[0]",
// "$.matrix[*][*]") is valid JSON Path but not expressible as a pushdown
// filter. It fails with a plain error that does NOT wrap ErrInvalidFilterPath,
// which is the signal to fall back to in-memory evaluation rather than to
// reject the request. A malformed one is invalid input, per the list above.
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
//   - PATTERN COMPILABILITY. An uncompilable MATCHES_PATTERN or LIKE operand
//     (e.g. LIKE with a trailing unpaired escape) leaves the compiled
//     program nil and the leaf silently returns false. Note the kernel
//     compiles the ANCHORED form of MATCHES_PATTERN while a naive
//     caller-side check would compile the raw operand, so the two accept
//     sets are not identical — use [ValidateLeafPattern] (per leaf) or
//     [ValidateConditionPatterns] (whole condition) rather than hand-rolling
//     the check; they route through the same derivation the kernel does.
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
	// FieldsMap keys are always "$."-prefixed, and stripDollarDot has just
	// guaranteed c.JsonPath is too, so this is an identity today. It is kept
	// as the single key-construction convention — arrayToFilter builds its key
	// through arrayElementPath, which normalises the same way — because a key
	// that misses the map does not fail loudly: Declared comes back empty and
	// the type-directed kernel expands a comparison leaf with no declared type
	// into nothing, so a field that exists and holds matching data answers
	// with an empty page.
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
//
// It is a CANONICALISER, not a validator: it says nothing about whether raw is
// a legal path, and adding a leader to a bare identifier here does not make
// that identifier an acceptable wire jsonPath — [ConditionToFilter] requires
// the leader on input and rejects a bare path outright.
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

// jsonPathLeader is the mandatory prefix of a wire jsonPath. See
// [stripDollarDot].
const jsonPathLeader = "$."

// stripDollarDot converts a wire jsonPath into the bare [Filter.Path] form,
// rejecting anything that is not the model's JSON Path syntax.
//
// # The two path forms
//
// These are different and easy to conflate:
//
//   - The WIRE form is what a caller writes in a condition's jsonPath. It is
//     JSON Path nomenclature and the "$." leader is REQUIRED: "$.amount".
//   - The PLUGIN-FACING form is [Filter.Path], which is BARE: "amount". This
//     function is the boundary between them; see Filter.Path's "Grammar"
//     section, which this enforces on the post-leader remainder.
//
// # Two error classes, and why the difference matters
//
// Every engine caller treats a translation error as "not pushdownable, fall
// back to in-memory evaluation". That is the right response to one kind of
// failure and badly wrong for the other, so the two are distinguishable:
//
//   - INVALID PATH — no "$." leader, nothing after it, an empty or trailing
//     segment, bracket-quoted property access, or any character outside the
//     grammar. The path is not JSON Path nomenclature at all; a bare
//     "variantId" is simply not a path. These wrap [ErrInvalidFilterPath] and
//     a caller should surface them as a client error (400). Falling back
//     instead would be worse than useless: the in-memory evaluator resolves a
//     bare path happily, so the mistake would never surface, while a
//     bracket-quoted one resolves to nothing and answers an empty page for a
//     field that exists.
//   - NOT PUSHDOWNABLE — a WELL-FORMED "$."-prefixed path using array
//     subscript syntax ("$.tags[*].name", "$.arr[0]"). Valid JSON Path, and
//     the in-memory evaluator serves it, so this stays a plain error and the
//     fallback is the correct response. Promoting it to ErrInvalidFilterPath
//     would turn working queries into 400s.
//
// A MALFORMED subscript ("$.a[", "$.a[0:2]", `$.a["x"]`, "$.a[0];DROP") is in
// the first class, not the second: the whole path is scanned, subscripts
// included, so bracket syntax outside the supported forms is invalid input.
// It used to land in the fallback class because the scan stopped at the first
// '[' and accepted whatever followed — and the in-memory evaluator resolves
// none of those spellings, so the request answered an empty page for a field
// that exists.
func stripDollarDot(path string) (string, error) {
	if !strings.HasPrefix(path, jsonPathLeader) {
		return "", invalidPathError(path,
			`must be a JSON Path: expected the "$." leader (e.g. "$.amount")`)
	}
	stripped := path[len(jsonPathLeader):]
	if stripped == "" {
		return "", invalidPathError(path, `addresses no field: nothing follows the "$." leader`)
	}
	// Bracket-quoted property access ("$.['x']", `$.a["b"]`, and "$['x']"
	// which fails the leader check above) denotes the same node as dotted
	// access but is not the model's syntax, and NO evaluator in the stack
	// resolves it — pushdown rejects it and the in-memory fallback misses,
	// answering an empty page for a field that exists. The subscript scan
	// below would reject these anyway; naming them first lets the diagnostic
	// say what to write instead.
	if containsBracketQuote(stripped) {
		return "", invalidPathError(path,
			`bracket-quoted property access is not supported; use dotted access (e.g. "$.a.b")`)
	}
	hasSubscript, err := scanWirePathBody(stripped, func(reason string) error {
		return invalidPathError(path, reason)
	})
	if err != nil {
		return "", err
	}
	if hasSubscript {
		// Well-formed array subscript/wildcard syntax: valid JSON Path, not
		// expressible as a pushdown filter. The unpushdownable class — a
		// plain error, so the caller falls back to in-memory evaluation.
		return "", fmt.Errorf("path %q contains non-pushdownable array-subscript syntax", path)
	}
	return stripped, nil
}

// containsBracketQuote reports whether p contains a bracket-quoted property
// access in either quoting style.
func containsBracketQuote(p string) bool {
	return strings.Contains(p, "['") || strings.Contains(p, "']") ||
		strings.Contains(p, `["`) || strings.Contains(p, `"]`)
}

// scanWirePathBody validates the leader-stripped remainder of a wire jsonPath
// against the segment grammar and reports whether it uses array-subscript
// syntax. Diagnostics are built by mkInvalid so each caller can attach its own
// sentinel and echo the full path.
//
//	body      = segment ( "." segment )*
//	segment   = name subscript*
//	name      = 1*( ALPHA / DIGIT / "_" / "-" )        ; ASCII only
//	subscript = "[" ( "*" / 1*DIGIT ) "]"
//
// The whole body is scanned. An earlier version stopped at the first '[' and
// accepted the remainder unread, which admitted unbalanced brackets, slices,
// unions, filter expressions, negative indices and arbitrary trailing garbage
// — none of which any evaluator in the stack resolves.
//
// Errors are reported for the FIRST offending position, so the diagnostic
// names the character the caller has to fix.
func scanWirePathBody(body string, mkInvalid func(reason string) error) (bool, error) {
	hasSubscript := false
	i, n := 0, len(body)
	for {
		nameStart := i
		for i < n && isPathNameByte(body[i]) {
			i++
		}
		if i == nameStart {
			if i == n {
				return false, mkInvalid("ends in a trailing dot")
			}
			switch body[i] {
			case '.':
				return false, mkInvalid("contains an empty path segment")
			case '[':
				return false, mkInvalid("has an array subscript with no field name before it")
			case ']':
				return false, mkInvalid(`contains an unmatched "]"`)
			default:
				return false, mkInvalid(disallowedCharReason(body[i:]))
			}
		}
		for i < n && body[i] == '[' {
			rel := strings.IndexByte(body[i:], ']')
			if rel < 0 {
				return false, mkInvalid("has an unclosed array subscript")
			}
			inner := body[i+1 : i+rel]
			if !isSupportedSubscript(inner) {
				return false, mkInvalid(fmt.Sprintf(
					"has an unsupported array subscript %q; only the wildcard [*] and a non-negative index (e.g. [0]) are supported",
					"["+inner+"]"))
			}
			hasSubscript = true
			i += rel + 1
		}
		if i == n {
			return hasSubscript, nil
		}
		switch body[i] {
		case '.':
			i++
			if i == n {
				return false, mkInvalid("ends in a trailing dot")
			}
		case ']':
			return false, mkInvalid(`contains an unmatched "]"`)
		default:
			return false, mkInvalid(disallowedCharReason(body[i:]))
		}
	}
}

// isPathNameByte reports whether b is admissible inside a path segment name.
func isPathNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' ||
		b >= '0' && b <= '9' || b == '_' || b == '-'
}

// isSupportedSubscript reports whether the text between "[" and "]" is one of
// the two forms the stack can resolve: the wildcard, or a non-negative decimal
// index (digits only — no sign, no whitespace, no exponent). Everything else (a
// slice, a union, a filter expression, a negative or signed index) has no
// equivalent in either evaluator.
//
// The engine's boundary check applies the same rule, reaching it through the
// predicate its in-memory evaluator uses to rewrite a subscript for gjson, so
// "accepted here" and "resolvable there" stay the same question.
// TestValidateCondition_PathGrammarMatchesSPI (cyoda-go) pins the two against
// each other.
func isSupportedSubscript(inner string) bool {
	if inner == "*" {
		return true
	}
	if inner == "" {
		return false
	}
	for i := 0; i < len(inner); i++ {
		if inner[i] < '0' || inner[i] > '9' {
			return false
		}
	}
	return true
}

// disallowedCharReason renders the diagnostic for the first rune of s, which
// the grammar does not admit. It decodes a rune rather than a byte so a
// non-ASCII character is echoed whole rather than as a mojibake fragment.
func disallowedCharReason(s string) string {
	r, _ := utf8.DecodeRuneInString(s)
	return fmt.Sprintf("contains disallowed character %q", r)
}

// invalidPathError builds the [ErrInvalidFilterPath]-wrapping diagnostic for a
// wire jsonPath outside the model's JSON Path syntax, echoing the offending
// path so the caller can correct it.
func invalidPathError(path, reason string) error {
	return fmt.Errorf("%w: jsonPath %q %s", ErrInvalidFilterPath, path, reason)
}

// MaxConditionDepth caps recursion in [ValidateConditionOperators] and
// [ValidateConditionPatterns] to defend against stack exhaustion from a deeply
// nested predicate tree. Client-facing parsers cap incoming requests at a
// smaller depth, but a programmatically constructed tree bypasses that and can
// otherwise nest arbitrarily. 256 is well above any realistic query and well
// below the stack-blow threshold.
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
// [ConditionToFilter] are deliberately not folded in: the object-operand and
// BETWEEN-arity checks are cheap local checks a caller can apply while
// walking its own input; the pattern-compilability check was blocked on
// reaching the kernel's own pattern derivation, which [ValidateLeafPattern]
// and [ValidateConditionPatterns] now expose. Passing this function is not
// the same as having validated the condition.
//
// Pattern operands are now covered by [ValidateConditionPatterns]. A
// pattern-cost bound (rejecting a syntactically valid but expensive pattern)
// remains a separate, unsettled concern, out of scope for both functions.
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

// ValidateConditionPatterns walks cond and validates every pattern operand
// against the kernel's own derivation, so a caller can reject the whole request
// at the boundary rather than mid-translation or, worse, discover it as an
// empty result page.
//
// It mirrors [ValidateConditionOperators]'s recursion and shares its depth cap.
// The two are complements and neither implies the other: this one checks
// pattern OPERANDS and passes a misspelled operator (MapOperator returns the
// zero FilterOp, which compiles no pattern); that one checks operator NAMES and
// ignores operands. Call both.
//
// Errors wrap [ErrInvalidPattern] and name the offending leaf by jsonPath (or,
// for a lifecycle leaf, by field) and the operator string the caller wrote
// (e.g. "MATCHES_PATTERN") — never the operand, and never the internal
// FilterOp spelling ([ValidateLeafPattern] uses that vocabulary; this one
// speaks the caller's).
func ValidateConditionPatterns(cond predicate.Condition) error {
	return validatePatternsAtDepth(cond, 0)
}

func validatePatternsAtDepth(cond predicate.Condition, depth int) error {
	if cond == nil {
		return nil
	}
	if depth >= MaxConditionDepth {
		return fmt.Errorf("condition depth exceeded (max %d)", MaxConditionDepth)
	}
	switch c := cond.(type) {
	case *predicate.SimpleCondition:
		return checkPattern(MapOperator(c.OperatorType), c.OperatorType, c.Value, c.JsonPath)
	case *predicate.LifecycleCondition:
		return checkPattern(MapOperator(c.OperatorType), c.OperatorType, c.Value, c.Field)
	case *predicate.GroupCondition:
		for _, child := range c.Conditions {
			if err := validatePatternsAtDepth(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	case *predicate.ArrayCondition:
		// Positional values become equality leaves; no pattern operand.
		return nil
	default:
		// Including FunctionCondition, which carries no operator.
		return nil
	}
}

// checkPattern names the leaf and the operator the caller wrote (opName, e.g.
// "MATCHES_PATTERN") — never the operand, and never the internal FilterOp
// spelling. It calls compileLeafPattern directly rather than
// [ValidateLeafPattern], which would name the FilterOp instead: one name per
// error, in the vocabulary this caller's own caller used.
func checkPattern(op FilterOp, opName string, value any, location string) error {
	if _, err := compileLeafPattern(op, value); err != nil {
		return fmt.Errorf("%s: %s: %w", location, opName, err)
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
