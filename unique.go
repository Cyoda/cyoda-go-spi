package spi

// UniqueKey is a model-level composite unique key over scalar leaf fields.
// Fields are ordered dotted JSONPath leaves (same form as the schema's field paths).
type UniqueKey struct {
	ID     string
	Fields []string
}

// UniqueClaim is a computed assertion: the store must guarantee no OTHER live
// entity in the same (tenant, model name, model version) holds the same
// (KeyID, Signature). Signature is an opaque, type-tagged canonical encoding.
type UniqueClaim struct {
	KeyID     string
	Signature string
}

// CompositeUniqueKeyCapable is OPTIONAL on a StoreFactory: advertises composite
// unique-key support. Absence (or false) = unsupported. Additive; NOT part of
// the StoreFactory interface.
type CompositeUniqueKeyCapable interface {
	SupportsCompositeUniqueKeys() bool
}
