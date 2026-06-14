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

// IterateOptions narrows or shifts the iteration window.
//
// Currently single-field; reserved as the extension point for future
// iteration knobs (e.g. ordering hints).
type IterateOptions struct {
	// PointInTime, when non-nil, requests a historical snapshot at the
	// given instant. Semantics match the rest of the SPI (read-committed
	// snapshot).
	PointInTime *time.Time
}
