package spi

// UniqueKey is a model-level composite unique key over scalar leaf fields.
// Fields are ordered dotted JSONPath leaves (same form as the schema's field paths).
//
// A store enforces the unique keys of an entity's model as its own model
// store holds them (ModelDescriptor.UniqueKeys), read at the moment it
// enforces them. No caller passes keys.
//
// A transaction's claims are computed at commit, from its final entities:
//
//  1. The commit reads, inside the committing transaction, the model of
//     every entity whose final write in the transaction is a save, one read
//     per distinct model, in a fixed order (model name, then version). A
//     model that does not exist or is not LOCKED fails the commit with
//     ErrModelNotLocked. An entity whose final write is a delete needs no
//     model, so a transaction that only deletes commits whatever the state
//     of its entities' models.
//  2. It releases the claims of every entity the transaction wrote or
//     deleted.
//  3. It computes the claims of every entity whose final write is a save
//     (ComputeClaims), from the bytes that write was given, never from a
//     re-read of stored data, with the model's keys as read in step 1. A
//     partial composite key fails the commit with ErrPartialUniqueKey.
//  4. It inserts those claims. A value claimed twice inside the
//     transaction, or held by an entity outside the transaction's writes
//     (committed, or claimed by a concurrent transaction that commits
//     first), fails the commit with ErrUniqueViolation.
//
// A refused commit stores nothing. A save inside a transaction does no claim
// work, so a swap of values between entities, a chain of moves and a claim
// before the holder's free all commit when the final state is unique. A
// write outside a transaction is its own transaction and does steps 1-4
// atomically with the write.
//
// A store whose transaction reads from a snapshot taken when it began may
// answer the retryable ErrConflict in step 1 instead, when an entity's model
// was created or changed after that snapshot; the retry gets the answer
// above.
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
