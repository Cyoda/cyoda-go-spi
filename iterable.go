package spi

import (
	"context"
	"time"
)

// Iterable is an optional capability on a storage backend that yields
// entities matching a filter, one at a time, with bounded memory.
//
// Semantics:
//   - Plugins push pushable parts of the filter into storage (SQL WHERE,
//     CQL index lookup); residual is applied inside Next() before yielding.
//   - A zero-value Filter means "yield all entities for the model"
//     (subject to opts).
//   - IterateOptions.OrderBy: empty means order is unspecified. A backend
//     whose async search is engine-executed MUST honour a non-empty
//     OrderBy. A backend whose async search is self-executing MAY reject a
//     non-empty OrderBy with a plain error — there is no refusal sentinel,
//     callers see whatever error the plugin returns. A non-empty OrderBy
//     with an ambient transaction is unsupported; Iterate MUST return an
//     error rather than silently ignoring the order.
//   - Overlay semantics: with an ambient transaction, the merged
//     (committed ∪ transaction write-set) view is snapshotted at Iterate()
//     call time. Mutating the transaction while its iterator is open is
//     forbidden — the visibility of entities such a mutation would add,
//     remove, or change is unspecified for that already-open iterator.
//   - Implementations MUST NOT hold a global write-blocking lock for the
//     lifetime of the iterator (e.g. by holding only short-lived row locks,
//     or by paging through a cursor).
//   - The iterator MUST observe ctx cancellation: the underlying driver
//     surfaces an error; the iterator reports it via Err() and Next()
//     returns false.
//   - No retry on transient driver errors — the plugin surfaces the first
//     error and ends iteration.
//   - Err() returns that error stickily; subsequent Next() calls return
//     false.
//   - Close() is idempotent.
//
// Iterable is optional SPI-wide, the same way Searcher is: the engine's
// streamed async-search and scoped-delete paths use it when the backing
// store implements it, and a store implementing neither Searcher nor
// Iterable runs the engine's documented in-process fallback (GetAll plus
// in-memory filtering).
//
// (ModelRef is hoisted as a first-class argument because iteration is
// always scoped to exactly one model; IterateOptions carries only knobs
// that vary across calls against the same model.)
type Iterable interface {
	Iterate(
		ctx context.Context,
		model ModelRef,
		filter Filter,
		opts IterateOptions,
	) (Iterator, error)
}

// Iterator yields entities one at a time. Standard Go iterator shape
// modeled after database/sql.Rows.
type Iterator interface {
	// Next advances the iterator. Returns false on end or sticky error.
	Next() bool
	// Entity returns the current row. Valid only after Next() == true.
	Entity() *Entity
	// Err returns the first error encountered. Sticky.
	Err() error
	// Close releases server resources. Idempotent.
	Close() error
}

// IterateOptions narrows, orders, and scopes the iteration window.
type IterateOptions struct {
	// PointInTime, when non-nil, requests a historical snapshot at the
	// given instant. Semantics match the rest of the SPI (read-committed
	// snapshot): the read is COMMITTED-ONLY and ignores any ambient
	// transaction, so it never yields that transaction's own uncommitted
	// writes — see EntityStore.GetAsAt for the full statement, and note that
	// bounding the query on a timestamp is not sufficient to achieve it.
	PointInTime *time.Time

	// OrderBy specifies the sort keys applied to yielded entities. Empty
	// means order is unspecified — this differs from Searcher, where an
	// empty OrderBy still yields the engine's canonical entity-ID order.
	// See the Iterable doc comment for which backends must honour a
	// non-empty OrderBy, and why a non-empty OrderBy with an ambient
	// transaction is an error.
	OrderBy []OrderSpec

	// TrackingRead, when true and a transaction is active, records the
	// entities this iteration yields into the transaction's read-set, so
	// commit-time first-committer-wins validates them. Default false: a
	// plain snapshot read that records nothing. No-op when no transaction
	// is active. Same rule as SearchOptions.TrackingRead.
	TrackingRead bool
}
