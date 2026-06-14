package spi_test

import (
	"context"
	"testing"
	"time"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// TestIterableContract verifies the Iterable/Iterator interfaces compile
// and the expected method set is present. Runtime behavior is tested by
// plugin implementations in their own repos.
func TestIterableContract(t *testing.T) {
	var _ spi.Iterable = (spi.Iterable)(nil)
	var _ spi.Iterator = (spi.Iterator)(nil)

	var opts spi.IterateOptions
	now := time.Now()
	opts.PointInTime = &now
	_ = opts

	// Verify Iterate signature
	var iter spi.Iterable
	if iter != nil {
		_, _ = iter.Iterate(context.Background(), spi.ModelRef{}, spi.Filter{}, spi.IterateOptions{})
	}
}
