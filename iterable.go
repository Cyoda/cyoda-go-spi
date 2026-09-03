package spi

import (
	"time"
)

// Iterator and IterateOptions support EntityStore.Iterate; the iteration
// contract those two types serve is stated in EntityStore.Iterate's godoc.

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
	// means order is unspecified — this differs from EntityStore.Search,
	// where an empty OrderBy still yields the engine's canonical entity-ID
	// order. See EntityStore.Iterate for which backends must honour a
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
