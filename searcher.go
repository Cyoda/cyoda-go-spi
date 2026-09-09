package spi

import (
	"time"
)

// SearchOptions configures EntityStore.Search: bounding, ordering and
// scoping. There is no Offset: direct search does not paginate (async
// search does, over its persisted result-ID list).
type SearchOptions struct {
	ModelName    string
	ModelVersion string
	PointInTime  *time.Time

	// Limit is a bounded-or-fail cap on the matched set. Limit >= 1 is
	// REQUIRED; Limit <= 0 is a contract violation and the implementation
	// MUST return an error. See EntityStore.Search's doc comment — the full
	// contract is load-bearing.
	Limit   int
	OrderBy []OrderSpec

	// TrackingRead, when true and a transaction is active, records the
	// entities this search returns into the transaction's read-set, so
	// commit-time first-committer-wins validates them (a FOR-SHARE / locking
	// read, implemented optimistically). Default false: a plain snapshot
	// predicate read that records nothing. No-op when no transaction is
	// active. In-transaction search never prevents phantoms regardless of
	// this flag (see cyoda-go's docs/CONSISTENCY.md).
	//
	// Returned, not scanned: a row the filter excludes is never handed to the
	// caller and MUST NOT be recorded, whichever layer excluded it — a
	// storage predicate or an in-process re-check. Recording a
	// scanned-but-excluded row aborts the transaction on a concurrent commit
	// it never had a reason to conflict with. IterateOptions.TrackingRead
	// carries the identical rule, per yielded row.
	TrackingRead bool
}

// OrderKind selects the canonical comparison applied to a sort key. For data
// paths and non-id meta fields, every backend (memory, sqlite, postgres,
// commercial) applies the same comparison for a given Kind (byte-order text,
// IEEE-754 numeric, bool false<true, chronological instant), so ordering on
// those fields matches across backends. It does NOT apply to entity-ID
// ordering: see OrderSpec for the Path="id" case, where Kind is ignored and
// the comparator is the engine's own canonical ID order instead. The zero
// value is OrderText (byte-order string comparison).
type OrderKind int

const (
	OrderText     OrderKind = iota // byte order: BINARY / COLLATE "C" / bytes.Compare
	OrderNumeric                   // IEEE-754 double
	OrderBool                      // false < true
	OrderTemporal                  // chronological instant (engine meta dates only)
)

// OrderSpec is one sort key. Path is a scalar leaf: a dotted data path
// (Source=SourceData) or a canonical meta field name (Source=SourceMeta) —
// one of: state, creationDate, lastUpdateTime, transitionForLatestSave,
// transactionId, id. Kind fixes the cross-backend comparison for every path
// except one: for Source=SourceMeta, Path="id" the comparator is the
// engine's canonical entity-ID order — one total, stable, deterministic
// order per engine, documented per backend, and NOT required to be
// identical across backends — and Kind is ignored for that path. Absent/null
// values sort last. When OrderBy is empty the default order is the engine's
// canonical entity-ID order (engine-specific, documented per backend).
type OrderSpec struct {
	Path   string
	Source FieldSource
	Desc   bool
	Kind   OrderKind
}
