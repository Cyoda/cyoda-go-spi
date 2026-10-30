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
	_ = (func(StoreFactory, context.Context) (ScheduledTaskStore, error))(StoreFactory.ScheduledTaskStore)

	_ = (func(ScheduledTaskStore, context.Context, ReconcileRequest) ([]ScheduledTask, error))(ScheduledTaskStore.ReconcileForEntity)
	_ = (func(ScheduledTaskStore, context.Context, TenantID, string, uuid.UUID) error)(ScheduledTaskStore.RemoveLife)
	_ = (func(ScheduledTaskStore, context.Context, TaskRef, bool) error)(ScheduledTaskStore.StampSegment)
	_ = (func(ScheduledTaskStore, context.Context, TenantID, []string) error)(ScheduledTaskStore.DeleteForEntities)
	_ = (func(ScheduledTaskStore, context.Context, TenantID, string, int, func(string, string) bool) error)(ScheduledTaskStore.DeleteForModel)
	_ = (func(ScheduledTaskStore, context.Context, TenantID, string) (*ScheduledTask, bool, error))(ScheduledTaskStore.Get)
	_ = (func(ScheduledTaskStore, context.Context, TenantID, ScheduledTaskQuery) (ScheduledTaskPage, error))(ScheduledTaskStore.Query)
	_ = (func(ScheduledTaskStore, context.Context, ClaimRequest) ([]ScheduledTask, error))(ScheduledTaskStore.ClaimDue)
	_ = (func(ScheduledTaskStore, context.Context, uuid.UUID) error)(ScheduledTaskStore.Heartbeat)
	_ = (func(ScheduledTaskStore, context.Context, uuid.UUID) error)(ScheduledTaskStore.RetireOwner)
	_ = (func(ScheduledTaskStore, context.Context, time.Duration) error)(ScheduledTaskStore.SweepOwners)
	_ = (func(ScheduledTaskStore, context.Context, uuid.UUID, []uuid.UUID) (int, error))(ScheduledTaskStore.GiveBackIdle)
	_ = (func(ScheduledTaskStore, context.Context, TaskRef) error)(ScheduledTaskStore.MarkUnsafe)
	_ = (func(ScheduledTaskStore, context.Context, TaskRef, Attempt) error)(ScheduledTaskStore.RecordAttempt)
	_ = (func(ScheduledTaskStore, context.Context, TaskRef, Failure) error)(ScheduledTaskStore.Fail)
	_ = (func(ScheduledTaskStore, context.Context) error)(ScheduledTaskStore.SweepMarks)
	_ = (func(ScheduledTaskStore, context.Context) (SchedulerActivityReport, error))(ScheduledTaskStore.SchedulerActivity)
}

// TestScheduledTaskStore_MethodSet pins that nothing else is on the
// interface: a store double with exactly these seventeen methods satisfies
// it, so a method added back fails the build.
func TestScheduledTaskStore_MethodSet(t *testing.T) {
	var _ ScheduledTaskStore = seventeenMethods{}
}

type seventeenMethods struct{}

func (seventeenMethods) ReconcileForEntity(context.Context, ReconcileRequest) ([]ScheduledTask, error) {
	return nil, nil
}
func (seventeenMethods) RemoveLife(context.Context, TenantID, string, uuid.UUID) error { return nil }
func (seventeenMethods) StampSegment(context.Context, TaskRef, bool) error             { return nil }
func (seventeenMethods) DeleteForEntities(context.Context, TenantID, []string) error   { return nil }
func (seventeenMethods) DeleteForModel(context.Context, TenantID, string, int, func(string, string) bool) error {
	return nil
}
func (seventeenMethods) Get(context.Context, TenantID, string) (*ScheduledTask, bool, error) {
	return nil, false, nil
}
func (seventeenMethods) Query(context.Context, TenantID, ScheduledTaskQuery) (ScheduledTaskPage, error) {
	return ScheduledTaskPage{}, nil
}
func (seventeenMethods) ClaimDue(context.Context, ClaimRequest) ([]ScheduledTask, error) {
	return nil, nil
}
func (seventeenMethods) Heartbeat(context.Context, uuid.UUID) error       { return nil }
func (seventeenMethods) RetireOwner(context.Context, uuid.UUID) error     { return nil }
func (seventeenMethods) SweepOwners(context.Context, time.Duration) error { return nil }
func (seventeenMethods) GiveBackIdle(context.Context, uuid.UUID, []uuid.UUID) (int, error) {
	return 0, nil
}
func (seventeenMethods) MarkUnsafe(context.Context, TaskRef) error             { return nil }
func (seventeenMethods) RecordAttempt(context.Context, TaskRef, Attempt) error { return nil }
func (seventeenMethods) Fail(context.Context, TaskRef, Failure) error          { return nil }
func (seventeenMethods) SweepMarks(context.Context) error                      { return nil }
func (seventeenMethods) SchedulerActivity(context.Context) (SchedulerActivityReport, error) {
	return SchedulerActivityReport{}, nil
}
