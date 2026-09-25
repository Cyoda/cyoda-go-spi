package spi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestScheduledTaskStore_InterfaceShape pins every method signature of
// ScheduledTaskStore. Compile-time only: a changed signature fails the
// build of this test.
func TestScheduledTaskStore_InterfaceShape(t *testing.T) {
	var _ = (StoreFactory)(nil)
	var _ func(StoreFactory, context.Context) (ScheduledTaskStore, error) = StoreFactory.ScheduledTaskStore

	var _ func(ScheduledTaskStore, context.Context, ReconcileRequest) ([]ScheduledTask, error) = ScheduledTaskStore.ReconcileForEntity
	var _ func(ScheduledTaskStore, context.Context, TenantID, string, uuid.UUID) error = ScheduledTaskStore.RemoveLife
	var _ func(ScheduledTaskStore, context.Context, TaskRef, bool) error = ScheduledTaskStore.StampSegment
	var _ func(ScheduledTaskStore, context.Context, TenantID, []string) error = ScheduledTaskStore.DeleteForEntities
	var _ func(ScheduledTaskStore, context.Context, TenantID, string, int, func(string, string) bool) error = ScheduledTaskStore.DeleteForModel
	var _ func(ScheduledTaskStore, context.Context, TenantID, string) (*ScheduledTask, bool, error) = ScheduledTaskStore.Get
	var _ func(ScheduledTaskStore, context.Context, TenantID, ScheduledTaskQuery) (ScheduledTaskPage, error) = ScheduledTaskStore.Query
	var _ func(ScheduledTaskStore, context.Context, ClaimRequest) ([]ScheduledTask, error) = ScheduledTaskStore.ClaimDue
	var _ func(ScheduledTaskStore, context.Context, uuid.UUID) error = ScheduledTaskStore.Heartbeat
	var _ func(ScheduledTaskStore, context.Context, uuid.UUID) error = ScheduledTaskStore.RetireOwner
	var _ func(ScheduledTaskStore, context.Context, time.Duration) error = ScheduledTaskStore.SweepOwners
	var _ func(ScheduledTaskStore, context.Context, uuid.UUID, []uuid.UUID) (int, error) = ScheduledTaskStore.GiveBackIdle
	var _ func(ScheduledTaskStore, context.Context, TaskRef) error = ScheduledTaskStore.MarkUnsafe
	var _ func(ScheduledTaskStore, context.Context, TaskRef, Attempt) error = ScheduledTaskStore.RecordAttempt
	var _ func(ScheduledTaskStore, context.Context, TaskRef, Failure) error = ScheduledTaskStore.Fail
	var _ func(ScheduledTaskStore, context.Context) error = ScheduledTaskStore.SweepMarks
}

// TestScheduledTaskStore_MethodSet pins that nothing else is on the
// interface: a store double with exactly these sixteen methods satisfies
// it, so a method added back fails the build.
func TestScheduledTaskStore_MethodSet(t *testing.T) {
	var _ ScheduledTaskStore = sixteenMethods{}
}

type sixteenMethods struct{}

func (sixteenMethods) ReconcileForEntity(context.Context, ReconcileRequest) ([]ScheduledTask, error) {
	return nil, nil
}
func (sixteenMethods) RemoveLife(context.Context, TenantID, string, uuid.UUID) error { return nil }
func (sixteenMethods) StampSegment(context.Context, TaskRef, bool) error             { return nil }
func (sixteenMethods) DeleteForEntities(context.Context, TenantID, []string) error   { return nil }
func (sixteenMethods) DeleteForModel(context.Context, TenantID, string, int, func(string, string) bool) error {
	return nil
}
func (sixteenMethods) Get(context.Context, TenantID, string) (*ScheduledTask, bool, error) {
	return nil, false, nil
}
func (sixteenMethods) Query(context.Context, TenantID, ScheduledTaskQuery) (ScheduledTaskPage, error) {
	return ScheduledTaskPage{}, nil
}
func (sixteenMethods) ClaimDue(context.Context, ClaimRequest) ([]ScheduledTask, error) {
	return nil, nil
}
func (sixteenMethods) Heartbeat(context.Context, uuid.UUID) error       { return nil }
func (sixteenMethods) RetireOwner(context.Context, uuid.UUID) error     { return nil }
func (sixteenMethods) SweepOwners(context.Context, time.Duration) error { return nil }
func (sixteenMethods) GiveBackIdle(context.Context, uuid.UUID, []uuid.UUID) (int, error) {
	return 0, nil
}
func (sixteenMethods) MarkUnsafe(context.Context, TaskRef) error             { return nil }
func (sixteenMethods) RecordAttempt(context.Context, TaskRef, Attempt) error { return nil }
func (sixteenMethods) Fail(context.Context, TaskRef, Failure) error          { return nil }
func (sixteenMethods) SweepMarks(context.Context) error                      { return nil }
