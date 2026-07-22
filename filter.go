package spi

// FilterOp defines a filter operation for search predicate pushdown.
type FilterOp string

const (
	FilterAnd FilterOp = "and"
	FilterOr  FilterOp = "or"

	FilterEq  FilterOp = "eq"
	FilterNe  FilterOp = "ne"
	FilterGt  FilterOp = "gt"
	FilterLt  FilterOp = "lt"
	FilterGte FilterOp = "gte"
	FilterLte FilterOp = "lte"

	FilterContains   FilterOp = "contains"
	FilterStartsWith FilterOp = "starts_with"
	FilterEndsWith   FilterOp = "ends_with"
	FilterLike       FilterOp = "like"

	FilterIsNull  FilterOp = "is_null"
	FilterNotNull FilterOp = "not_null"

	FilterBetween      FilterOp = "between"
	FilterMatchesRegex FilterOp = "matches_regex"

	FilterIEq            FilterOp = "ieq"
	FilterINe            FilterOp = "ine"
	FilterIContains      FilterOp = "icontains"
	FilterINotContains   FilterOp = "inot_contains"
	FilterIStartsWith    FilterOp = "istarts_with"
	FilterINotStartsWith FilterOp = "inot_starts_with"
	FilterIEndsWith      FilterOp = "iends_with"
	FilterINotEndsWith   FilterOp = "inot_ends_with"
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
// Branch nodes (FilterAnd, FilterOr) carry Children.
type Filter struct {
	Op       FilterOp
	Path     string
	Source   FieldSource
	Value    any
	Values   []any
	Children []Filter
	Coercion FilterCoercion // temporal comparison routing (zero = CoerceNone)
}
