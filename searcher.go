package spi

import (
	"context"
	"time"
)

// Searcher is an optional interface for storage plugins that support
// search predicate pushdown (e.g. SQL WHERE clauses). Plugins that
// implement Searcher get native query execution; those that don't
// fall back to in-memory filtering.
//
// Search is bounded-or-fail. SearchOptions.Limit >= 1 is REQUIRED: it is a
// cap on the matched set, not a page size. An implementation that finds more
// matches than Limit MUST return ErrSearchResultLimitExceeded and MUST NOT
// return a truncated prefix — a silently truncated result is a wrong answer
// the caller cannot distinguish from a complete one. Exactly-at-limit
// succeeds.
//
// Limit <= 0 is a contract violation: the implementation MUST return an
// error rather than treating it as "unbounded" or substituting a default of
// its own. The engine resolves the direct-search default before calling, so
// Search itself never needs to guess a bound.
//
// Search MUST honour an active transaction (read-your-own-writes): with no
// transaction active it is a committed pushdown; with a transaction active it
// overlays the transaction's write-set so the result is identical to what
// GetAll + in-memory match would produce. In-transaction point-in-time reads
// are committed-only — they never see the transaction's own uncommitted
// writes for the PIT dimension. Returned entities enter the transaction's
// read-set only when SearchOptions.TrackingRead is set; under bounded-or-fail
// that is exactly the matched set, since there is no page smaller than it.
type Searcher interface {
	Search(ctx context.Context, filter Filter, opts SearchOptions) ([]*Entity, error)
}

// SearchOptions configures bounding, ordering, and scoping for a search.
// There is no Offset: direct search does not paginate (async search does,
// over its persisted result-ID list).
type SearchOptions struct {
	ModelName    string
	ModelVersion string
	PointInTime  *time.Time

	// Limit is a bounded-or-fail cap on the matched set. Limit >= 1 is
	// REQUIRED; Limit <= 0 is a contract violation and the implementation
	// MUST return an error. See the Searcher doc comment — the full contract
	// is load-bearing.
	Limit   int
	OrderBy []OrderSpec

	// TrackingRead, when true and a transaction is active, records the
	// entities this search returns into the transaction's read-set, so
	// commit-time first-committer-wins validates them (a FOR-SHARE / locking
	// read, implemented optimistically). Default false: a plain snapshot
	// predicate read that records nothing. No-op when no transaction is
	// active. In-transaction search never prevents phantoms regardless of
	// this flag (see docs/CONSISTENCY.md).
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
