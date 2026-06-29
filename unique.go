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
