package spi

// UniqueKey is a model-level composite unique key over scalar leaf fields.
// Fields are ordered dotted JSONPath leaves (same form as the schema's field paths).
//
// A store enforces the unique keys of an entity's model as its own model
// store holds them (ModelDescriptor.UniqueKeys), read at commit. No caller
// passes keys.
//
// The caller changes a model's state or unique keys (save, lock, unlock)
// only while no transaction that will write entities of that model is
// open. A store may read the keys inside the committing transaction, so if
// the caller breaks this, the commit of such a transaction may enforce the
// keys as they were when it began.
//
// A transaction's claims are computed at commit, from its final entities:
//
//  1. The commit reads the unique keys of the model of every entity whose
//     final write in the transaction is a save, one read per distinct
//     model, from its stored model. The model's state is not checked: the
//     caller saves entities only into a LOCKED model (see ModelStore). An
//     entity whose final write is a delete needs no model read.
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
// Two commits that each claim a value the other frees in the same commit
// can deadlock in a store that inserts claims under row locks. One of them
// fails with the retryable ErrConflict, and the other with
// ErrUniqueViolation, because the value it claims is still held when the
// first rolls back; the retry gets ErrUniqueViolation.
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
