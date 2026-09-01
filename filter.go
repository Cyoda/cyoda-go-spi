package spi

// FilterOp defines a filter operation for search predicate pushdown.
type FilterOp string

const (
	FilterAnd FilterOp = "and"
	FilterOr  FilterOp = "or"

	// FilterNot negates its single child (see Filter.Children). It is NOT
	// De Morgan sugar for "invert the operator and distribute": over a
	// wildcard path it is a universal quantifier ("no element satisfies the
	// child"), a different question from applying the child's negative
	// counterpart element-wise ("some element differs" — see
	// docs/cloud-parity/path-grammar.md section 5 and prepared_filter.go's
	// match doc). NOT of a leaf that is false for every reason — including a
	// vacuous one: an empty array, an explicit null, or an absent field —
	// is true.
	FilterNot FilterOp = "not"

	FilterEq  FilterOp = "eq"
	FilterNe  FilterOp = "ne"
	FilterGt  FilterOp = "gt"
	FilterLt  FilterOp = "lt"
	FilterGte FilterOp = "gte"
	FilterLte FilterOp = "lte"

	FilterContains   FilterOp = "contains"
	FilterStartsWith FilterOp = "starts_with"
	FilterEndsWith   FilterOp = "ends_with"

	// FilterLike is a glob, not a regex. The grammar, which is PostgreSQL's
	// and SQLite's `LIKE ... ESCAPE '\'`:
	//
	//   - '%'  any sequence of characters, including empty, INCLUDING newlines
	//   - '_'  exactly one character (one rune), INCLUDING a newline
	//   - '\X' the literal character X, for ANY X — so \%, \_ and \\ are literal
	//     '%', '_' and '\', and \d is a literal 'd'
	//   - anything else, itself
	//
	// The match is whole-string and case-sensitive. A trailing unpaired '\' is
	// the only malformed pattern; every other operand matches something.
	//
	// This is deliberately NOT Cloud's Like.prepareSpecialCharacters, which
	// translates to a regex and leaks RE2 escapes. cyoda-go leads this
	// contract.
	FilterLike FilterOp = "like"

	FilterIsNull  FilterOp = "is_null"
	FilterNotNull FilterOp = "not_null"

	FilterBetween          FilterOp = "between"
	FilterBetweenInclusive FilterOp = "between_inclusive"
	FilterMatchesRegex     FilterOp = "matches_regex"

	FilterIEq            FilterOp = "ieq"
	FilterINe            FilterOp = "ine"
	FilterIContains      FilterOp = "icontains"
	FilterINotContains   FilterOp = "inot_contains"
	FilterNotContains    FilterOp = "not_contains"
	FilterIStartsWith    FilterOp = "istarts_with"
	FilterINotStartsWith FilterOp = "inot_starts_with"
	FilterNotStartsWith  FilterOp = "not_starts_with"
	FilterIEndsWith      FilterOp = "iends_with"
	FilterINotEndsWith   FilterOp = "inot_ends_with"
	FilterNotEndsWith    FilterOp = "not_ends_with"
)

// FieldSource indicates whether a filter path refers to entity data or metadata.
type FieldSource string

const (
	SourceData FieldSource = "data"
	SourceMeta FieldSource = "meta"
)

// FilterCoercion selects the comparison semantics for a leaf, mirroring
// OrderSpec.Kind for sort. CoerceNone (zero value) preserves the existing
// numeric/text/bool evaluation; CoerceTemporal compares as floored epoch-ms
// instants. The domain layer stamps this from the model schema / meta type;
// backends consume it without inspecting the value. Polymorphic-temporal body
// typing reuses this marker unchanged — it adds no new coercion value.
type FilterCoercion int

const (
	CoerceNone FilterCoercion = iota
	CoerceTemporal
)

// Filter is a generic predicate tree for search pushdown.
// Leaf nodes carry Op, Path, Source, and Value/Values.
// Branch nodes (FilterAnd, FilterOr) carry Children of any length (zero is
// the identity: empty AND matches everything, empty OR matches nothing).
// FilterNot is also a branch node but is arity-exactly-one: Children of
// length 0, length >= 2, or whose single element has a zero Op, all fail
// [Prepare] rather than being guessed at — there is no well-defined way to
// invert an empty or many-child set, and Filter is a public struct any
// backend can build, so Prepare cannot trust one to already be well-formed.
type Filter struct {
	Op FilterOp

	// Path addresses the leaf field this predicate applies to. It is BARE:
	// there is no "$." prefix and no JSONPath syntax. [ConditionToFilter]
	// strips the "$." at the wire boundary (see stripDollarDot, where the
	// leader is mandatory on the way in) and [lifecycleToFilter] emits
	// canonical meta names directly, so by the time a Filter reaches a storage
	// plugin the prefix is already gone. A "$."-prefixed path is therefore
	// malformed, not a tolerated alias.
	//
	// The two forms are opposites and must not be conflated: the wire jsonPath
	// REQUIRES the leader, this plugin-facing Path FORBIDS it.
	//
	// # Grammar
	//
	//	path      = segment ( "." segment )*
	//	segment   = name subscript*
	//	name      = 1*( ALPHA / DIGIT / "_" / "-" )   ; ASCII only
	//	subscript = "[" ( "*" / 1*DIGIT ) "]"
	//
	// This is the wire jsonPath grammar with the "$." leader removed. A
	// bracket is an array subscript; a dotted numeric segment is a field
	// whose name is that digit string. The two address different values and
	// a backend MUST NOT collapse them — see docs/cloud-parity/path-grammar.md.
	//
	// The grammar is deliberately narrower than any backend's native JSON
	// path syntax. It is the intersection every backend can serve, and on
	// SQL backends it is also the injection guard: every character that could
	// terminate a quoted JSON-path literal is outside it. A backend needing a
	// wider form must widen this grammar, not bypass its own validator.
	//
	// An EMPTY Path is legal ONLY for a tree operator (FilterAnd, FilterOr):
	// those carry no leaf condition of their own, Children hold the real
	// leaves. It is NOT a way for a LEAF to say "addresses no field" — every
	// leaf addresses exactly one field, on either FieldSource, and an empty
	// Path on one is rejected: spi.Prepare fails it with ErrUnevaluableLeaf
	// rather than treating it as a match against the whole document or as an
	// unconditional non-match. See prepared_filter.go.
	//
	// Parse it with ParseFilterPath and validate it with ValidateFilterPath.
	// A second, independent spelling of the grammar is how a backend admits a
	// form no resolver serves. This grammar and these two parse helpers are
	// for Source=SourceData; a Source=SourceMeta Path is not a data path at
	// all — it names one of a closed set of canonical meta field names
	// directly (a superset of [MetaFieldNames] that also carries storage-key
	// aliases such as "entity_id"), and spi.Prepare rejects a Path that is
	// empty or outside that set the same way (ErrUnevaluableLeaf).
	//
	// # Rejection is mandatory
	//
	// Both FieldSource values are held to the same grammar, and the check is
	// on the whole tree — a malformed path nested under an and/or branch is
	// still malformed.
	//
	// A backend MUST reject a malformed non-empty Path with an error. It MUST
	// NOT answer with an empty result set: a path the caller mistyped and a
	// predicate that genuinely matched nothing are different answers, and a
	// backend that conflates them makes a client error indistinguishable from
	// a legitimate empty page on that backend alone. Backends name this
	// sentinel ErrInvalidFilterPath. The in-process kernel (spi.Prepare)
	// enforces the same rejection — of an empty or malformed SourceData path,
	// and of an empty or non-vocabulary SourceMeta path — via
	// ErrUnevaluableLeaf, ahead of any backend-specific validation.
	Path string

	Source   FieldSource
	Value    any
	Values   []any
	Children []Filter
	Coercion FilterCoercion // temporal comparison routing (zero = CoerceNone)
	// Declared holds the leaf field's declared model DataTypes, stamped by the
	// domain layer from the model schema; the kernel uses them for
	// type-directed comparison. Empty for as-yet-unstamped or non-typed leaves.
	Declared []DataType
}
