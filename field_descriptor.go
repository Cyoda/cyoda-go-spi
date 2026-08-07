package spi

// FieldDescriptor is a flat representation of a single leaf field in a model's
// schema tree — one entry of the flattened "fields map" keyed by JSONPath.
//
// It lives here, rather than in the engine's model package, for the same
// reason DataType does (see datatype.go): the search leaf-comparison kernel
// and [ConditionToFilter] both need it, and a storage plugin that
// self-executes a search must be able to build one.
//
// The engine currently declares its own structurally identical
// schema.FieldDescriptor. The intent is for that to become a type alias for
// this one so there is a single definition, but until the engine change lands
// the two are independent and must be kept in sync.
type FieldDescriptor struct {
	// Path is the leaf's key in the flattened field view, in the model
	// tree's JSONPath convention: a "$." prefix, and array hops rendered as
	// "[*]" (e.g. "$.name", "$.items[*].price"). The convention is
	// load-bearing, not cosmetic — a lookup that misses yields empty Types,
	// which the kernel treats as described on [ConditionToFilter].
	Path string

	// Types is the declared type set for the leaf — what [Filter.Declared]
	// carries and what the kernel dispatches on. Empty is meaningful and
	// dangerous: see [ConditionToFilter].
	Types []DataType

	// IsArray marks a leaf reached directly as an array's element type.
	IsArray bool

	// MaxWidth is an observed-width statistic from model discovery. It is
	// NOT populated on the read path: the stored schema wire format carries
	// no width, so a descriptor obtained via [FieldsMapFromSchema] always
	// reports 0. Only the engine's discovery-time tree populates it.
	MaxWidth int
}
