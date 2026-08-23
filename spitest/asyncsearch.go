package spitest

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func runAsyncSearchSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "CreateAndGet", testASCreateAndGet)
	runSubtest(t, h, tracker, "GetJob/NotFound", testASGetJobNotFound)
	runSubtest(t, h, tracker, "UpdateStatus/Succeeded", testASUpdateSucceeded)
	runSubtest(t, h, tracker, "UpdateStatus/Failed", testASUpdateFailed)
	runSubtest(t, h, tracker, "SaveAndGetResults/Pagination", testASResultsPagination)
	runSubtest(t, h, tracker, "Cancel", testASCancel)
	runSubtest(t, h, tracker, "Cancel/NotFound", testASCancelNotFound)
	runSubtest(t, h, tracker, "DeleteJob", testASDeleteJob)
	runSubtest(t, h, tracker, "ReapExpired", testASReapExpired)
	runSubtest(t, h, tracker, "ReapExpired/CancelledIsReapable", testASReapExpiredCancelledIsReapable)
	runSubtest(t, h, tracker, "TenantIsolation", testASTenantIsolation)
}

func newSearchJob(tenantID spi.TenantID, id string) *spi.SearchJob {
	return &spi.SearchJob{
		ID:         id,
		TenantID:   tenantID,
		Status:     "RUNNING",
		ModelRef:   spi.ModelRef{EntityName: "m1", ModelVersion: "1"},
		CreateTime: time.Now().UTC(),
	}
}

func testASCreateAndGet(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, err := h.Factory.AsyncSearchStore(ctx)
	require.NoError(t, err)
	id := newID()
	job := newSearchJob(tid, id)
	require.NoError(t, as.CreateJob(ctx, job))
	got, err := as.GetJob(ctx, id)
	require.NoError(t, err)
	require.Equal(t, id, got.ID)
	require.Equal(t, tid, got.TenantID)
}

func testASGetJobNotFound(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	as, _ := h.Factory.AsyncSearchStore(ctx)
	_, err := as.GetJob(ctx, newID()) // valid UUID, never written
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testASUpdateSucceeded(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(tid, id)))
	finish := time.Now().UTC()
	require.NoError(t, as.UpdateJobStatus(ctx, id, 1, "SUCCESSFUL", 42, "", finish, 100))
	got, err := as.GetJob(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "SUCCESSFUL", got.Status)
	require.Equal(t, 42, got.ResultCount)
	require.Equal(t, int64(100), got.CalcTimeMs)
	require.NotNil(t, got.FinishTime)
}

func testASUpdateFailed(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(tid, id)))
	require.NoError(t, as.UpdateJobStatus(ctx, id, 1, "FAILED", 0, "boom", time.Now().UTC(), 10))
	got, _ := as.GetJob(ctx, id)
	require.Equal(t, "FAILED", got.Status)
	require.Equal(t, "boom", got.Error)
}

func testASResultsPagination(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(tid, id)))
	// Use UUID-based IDs to satisfy backends that store result IDs as timeuuids
	// (e.g. Cassandra). Short literals like "a","b","c" are not valid UUIDs.
	ids := []string{newID(), newID(), newID(), newID(), newID(), newID(), newID(), newID()}
	require.NoError(t, as.SaveResults(ctx, id, 1, slices.Values(ids)))

	page1, total, err := as.GetResultIDs(ctx, id, 0, 3)
	require.NoError(t, err)
	require.Equal(t, 8, total)
	require.Equal(t, ids[0:3], page1)

	page2, total, err := as.GetResultIDs(ctx, id, 3, 3)
	require.NoError(t, err)
	require.Equal(t, 8, total)
	require.Equal(t, ids[3:6], page2)
}

func testASCancel(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(tid, id)))

	ft := h.Now()
	require.NoError(t, as.Cancel(ctx, id, ft))
	got, err := as.GetJob(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "CANCELLED", got.Status)
	require.NotNil(t, got.FinishTime)

	// Idempotent: re-cancelling a terminal job with a LATER time is a no-op
	// that must not overwrite the original finish time.
	later := ft.Add(1 * time.Hour)
	require.NoError(t, as.Cancel(ctx, id, later), "re-cancelling a terminal job is a no-op")
	got2, err := as.GetJob(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got2.FinishTime)
	require.True(t, got.FinishTime.Equal(*got2.FinishTime), "re-cancel must not overwrite the original finish time")
}

func testASCancelNotFound(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	as, _ := h.Factory.AsyncSearchStore(ctx)
	err := as.Cancel(ctx, newID(), h.Now()) // valid UUID, never written
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testASDeleteJob(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(tid, id)))
	require.NoError(t, as.DeleteJob(ctx, id))
	_, err := as.GetJob(ctx, id)
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testASReapExpired(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(tid, id)))
	// Move the job to a terminal state so ReapExpired considers it eligible.
	// Running jobs are intentionally skipped by the reaper (they may still
	// have live goroutines writing results).
	finishTime := h.Now().UTC()
	require.NoError(t, as.UpdateJobStatus(ctx, id, 1, "SUCCESSFUL", 0, "", finishTime, 0))

	ttl := 10 * time.Millisecond
	h.AdvanceClock(ttl + 1*time.Millisecond)
	n, err := as.ReapExpired(ctx, ttl)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)
	_, err = as.GetJob(ctx, id)
	require.ErrorIs(t, err, spi.ErrNotFound)
}

// testASReapExpiredCancelledIsReapable is the acceptance that a cancelled
// job — not just a SUCCESSFUL/FAILED one — is reapable, i.e. Cancel stamps a
// finish time on the transition it performs.
func testASReapExpiredCancelledIsReapable(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(tid, id)))
	require.NoError(t, as.Cancel(ctx, id, h.Now()))

	ttl := 10 * time.Millisecond
	h.AdvanceClock(ttl + 1*time.Millisecond)
	n, err := as.ReapExpired(ctx, ttl)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)
	_, err = as.GetJob(ctx, id)
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testASTenantIsolation(t *testing.T, h Harness) {
	tA, tB := h.NewTenant(), h.NewTenant()
	id := newID()
	asA, _ := h.Factory.AsyncSearchStore(tenantContext(tA))
	asB, _ := h.Factory.AsyncSearchStore(tenantContext(tB))
	require.NoError(t, asA.CreateJob(tenantContext(tA), newSearchJob(tA, id)))
	_, err := asB.GetJob(tenantContext(tB), id)
	require.ErrorIs(t, err, spi.ErrNotFound)
}
