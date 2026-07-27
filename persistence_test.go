package spi

import (
	"context"
	"testing"
)

func TestScheduledTaskStore_InterfaceShape(t *testing.T) {
	// Compile-time only: a nil typed factory must satisfy the accessor.
	var _ = (StoreFactory)(nil)
	f := func(sf StoreFactory, ctx context.Context) (ScheduledTaskStore, error) {
		return sf.ScheduledTaskStore(ctx)
	}
	_ = f
	var _ ReconcileRequest
}
