package spi

import (
	"context"
	"testing"
)

func TestScheduledTaskStore_InterfaceShape(t *testing.T) {
	// Compile-time only: a nil typed factory must satisfy the accessor.
	var _ StoreFactory = (StoreFactory)(nil)
	var f func(StoreFactory, context.Context) (ScheduledTaskStore, error) =
		func(sf StoreFactory, ctx context.Context) (ScheduledTaskStore, error) {
			return sf.ScheduledTaskStore(ctx)
		}
	_ = f
	var _ ReconcileRequest
}
