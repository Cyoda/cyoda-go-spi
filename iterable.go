package spi

import (
	"context"
	"errors"
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
//     lifetime of the iterator (snapshot-then-iterate or cursor-based).
//   - The iterator MUST observe ctx cancellation: the underlying driver
//     surfaces an error; the iterator reports it via Err() and Next()
//     returns false.
//   - No retry on transient driver errors. First error is sticky.
//   - Close() is idempotent.
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
type IterateOptions struct {
	// PointInTime, when non-nil, requests a historical snapshot at the
	// given instant. Semantics match the rest of the SPI (read-committed
	// snapshot).
	PointInTime *time.Time
}

// ErrGroupCardinalityExceeded is returned by GroupedAggregator
// implementations (or surfaced by the service-layer streaming tally)
// when the result group count would exceed the configured ceiling.
var ErrGroupCardinalityExceeded = errors.New("group cardinality exceeded ceiling")

// ErrAggregationNotPushdownable signals that a GroupedAggregator
// implementation cannot safely push down a specific request shape; the
// caller (typically the service layer) should fall through to the
// streaming-tally path via Iterable.
var ErrAggregationNotPushdownable = errors.New("aggregation not pushdownable on this backend")
