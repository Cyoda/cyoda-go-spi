package spi_test

import (
	"testing"
	"time"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// TestIterableContract pins SPI field layout at SPI-consumer compile time.
// Runtime semantics are exercised by plugin parity tests in cyoda-go's
// e2e/parity registry; the Iterable/Iterator interface compile checks are
// already enforced by the package compiling.
func TestIterableContract(t *testing.T) {
	// IterateOptions field-layout check (catches renames at SPI-consumer
	// compile time even when the consumer doesn't yet exercise the field).
	var opts spi.IterateOptions
	now := time.Now()
	opts.PointInTime = &now
	if opts.PointInTime != &now {
		t.Fatalf("PointInTime round-trip mismatch")
	}
}
