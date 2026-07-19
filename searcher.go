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
// Search MUST honour an active transaction (read-your-own-writes): with no
// transaction active it is a committed pushdown; with a transaction active it
// overlays the transaction's write-set so the result is identical to what
// GetAll + in-memory match would produce. In-transaction point-in-time reads
// are committed-only — they never see the transaction's own uncommitted
// writes for the PIT dimension. Returned entities enter the transaction's
// read-set only when SearchOptions.TrackingRead is set.
type Searcher interface {
	Search(ctx context.Context, filter Filter, opts SearchOptions) ([]*Entity, error)
}

// SearchOptions configures pagination, ordering, and scoping for a search.
type SearchOptions struct {
	ModelName    string
	ModelVersion string
	PointInTime  *time.Time
	Limit        int
	Offset       int
	OrderBy      []OrderSpec

	// TrackingRead, when true and a transaction is active, records the
	// entities this search returns into the transaction's read-set, so
	// commit-time first-committer-wins validates them (a FOR-SHARE / locking
	// read, implemented optimistically). Default false: a plain snapshot
	// predicate read that records nothing. No-op when no transaction is
	// active. In-transaction search never prevents phantoms regardless of
	// this flag (see docs/CONSISTENCY.md).
	TrackingRead bool
}

// OrderKind selects the canonical comparison applied to a sort key so that
// every backend (memory, sqlite, postgres, commercial) produces identical
// ordering. The zero value is OrderText (byte-order string comparison).
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
// transactionId, id. Kind fixes the cross-backend comparison. Absent/null
// values sort last. When OrderBy is empty the default order is entity_id asc.
type OrderSpec struct {
	Path   string
	Source FieldSource
	Desc   bool
	Kind   OrderKind
}
