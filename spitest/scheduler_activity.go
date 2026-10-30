package spitest

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func schedulerActivity(t *testing.T, f *stFixture) spi.SchedulerActivityReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), stWait)
	defer cancel()
	rep, err := f.sts.SchedulerActivity(ctx)
	require.NoError(t, err)
	return rep
}

// freshStoreHarness returns h with Factory replaced by a fresh store from
// Harness.ClockFactory, closed when the subtest ends. SchedulerActivity reads
// every tenant, so these cases run on a store no other case has written to.
// It skips the subtest when the harness has no ClockFactory.
func freshStoreHarness(t *testing.T, h Harness) Harness {
	t.Helper()
	return newClockStore(t, h, h.Now().UTC()).h
}

// A store holding no task reports none.
func testSTSchedulerActivityEmpty(t *testing.T, h Harness) {
	f := newSTFixture(t, freshStoreHarness(t, h))
	rep := schedulerActivity(t, f)
	require.False(t, rep.HasWaiting, "no task is WAITING")
	require.False(t, rep.Running, "no task is RUNNING")
	require.Zero(t, rep.EarliestWaitingMs, "EarliestWaitingMs is 0 when HasWaiting is false")
}

// The report spans tenants: the earliest WAITING NextAttemptTime of any of
// them, whether any task is RUNNING, and nothing from a FAILED task.
func testSTSchedulerActivityEarliestAcrossTenants(t *testing.T, h Harness) {
	const (
		failedAt  int64 = 1_000
		earlyAt   int64 = 2_000
		lateAt    int64 = 3_000
		claimAtF  int64 = 1_500 // due: only the task at failedAt
		claimAtEA int64 = 2_500 // due: the task at earlyAt, not the one at lateAt
	)
	h = freshStoreHarness(t, h)
	fLate, fEarly, fFailed := newSTFixture(t, h), newSTFixture(t, h), newSTFixture(t, h)
	fLate.arm(lateAt)
	early := fEarly.arm(earlyAt)

	rep := schedulerActivity(t, fLate)
	require.True(t, rep.HasWaiting)
	require.Equal(t, earlyAt, rep.EarliestWaitingMs, "the earliest WAITING task of any tenant")
	require.False(t, rep.Running)

	// A FAILED task counts for neither field, though it is the earliest.
	failed := fFailed.arm(failedAt)
	reqF := fFailed.claimReq(uuid.New(), false)
	reqF.NowMs = claimAtF
	claimedF := fFailed.claimOnly(reqF, failed.ID)
	require.NoError(t, fFailed.sts.Fail(fFailed.ctx, stRef(claimedF),
		spi.Failure{Reason: spi.FailureRunPanicked, Error: "E", AtMs: claimAtF}))
	rep = schedulerActivity(t, fLate)
	require.True(t, rep.HasWaiting)
	require.Equal(t, earlyAt, rep.EarliestWaitingMs, "a FAILED task is ignored")
	require.False(t, rep.Running, "a FAILED task is not RUNNING")

	// Claiming the earliest makes it RUNNING and leaves the late one the
	// earliest WAITING.
	reqE := fEarly.claimReq(uuid.New(), false)
	reqE.NowMs = claimAtEA
	fEarly.claimOnly(reqE, early.ID)
	rep = schedulerActivity(t, fLate)
	require.True(t, rep.Running, "a claimed task is RUNNING")
	require.True(t, rep.HasWaiting)
	require.Equal(t, lateAt, rep.EarliestWaitingMs, "a RUNNING task is not WAITING")
}
