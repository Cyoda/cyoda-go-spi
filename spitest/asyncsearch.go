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
	runSubtest(t, h, tracker, "Epoch/InitialisedToOne", testASEpochInitialisedToOne)
	runSubtest(t, h, tracker, "Epoch/FencedWrites", testASEpochFencedWrites)
	runSubtest(t, h, tracker, "Terminal/WriteOnce", testASTerminalWriteOnce)
	runSubtest(t, h, tracker, "Claim/StaleClaimed", testASClaimStaleClaimed)
	runSubtest(t, h, tracker, "Claim/FreshNotClaimed", testASClaimFreshNotClaimed)
	runSubtest(t, h, tracker, "Claim/NilHeartbeatBaseline", testASClaimNilHeartbeatBaseline)
	runSubtest(t, h, tracker, "Claim/ConcurrentDisjoint", testASClaimConcurrentDisjoint)
	runSubtest(t, h, tracker, "Claim/TerminalNeverClaimed", testASClaimTerminalNeverClaimed)
	runSubtest(t, h, tracker, "ClearResults/Idempotent", testASClearResultsIdempotent)
	runSubtest(t, h, tracker, "SaveResults/ChunkSeqContinuity", testASSaveResultsChunkSeqContinuity)
	runSubtest(t, h, tracker, "GetResultIDs/DegenerateInputs", testASGetResultIDsDegenerateInputs)
	runSubtest(t, h, tracker, "GetResultIDs/NonTerminalPartial", testASGetResultIDsNonTerminalPartial)
	runSubtest(t, h, tracker, "UpdateStatus/MissingIsNotFound", testASUpdateStatusMissingIsNotFound)
	runSubtest(t, h, tracker, "UpdateStatus/ZeroFinishTimeAbsent", testASUpdateStatusZeroFinishTimeAbsent)
	runSubtest(t, h, tracker, "Heartbeat/Semantics", testASHeartbeatSemantics)
}

// newSearchJob stamps CreateTime from the harness clock (h.Now), not real
// wall-clock time. CreateTime is the fallback baseline ClaimStale compares
// against the store's own (possibly virtual) clock when HeartbeatTime is
// nil; stamping it from a different clock domain than h.AdvanceClock drives
// makes the staleness cutoff unreachable on backends with real per-operation
// latency. See search_store.go's ClaimStale doc: "The staleness stamp and
// the staleness comparison use the same clock domain."
func newSearchJob(h Harness, tenantID spi.TenantID, id string) *spi.SearchJob {
	return &spi.SearchJob{
		ID:         id,
		TenantID:   tenantID,
		Status:     "RUNNING",
		ModelRef:   spi.ModelRef{EntityName: "m1", ModelVersion: "1"},
		CreateTime: h.Now().UTC(),
	}
}

func testASCreateAndGet(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, err := h.Factory.AsyncSearchStore(ctx)
	require.NoError(t, err)
	id := newID()
	job := newSearchJob(h, tid, id)
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
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
	finish := h.Now().UTC()
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
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
	require.NoError(t, as.UpdateJobStatus(ctx, id, 1, "FAILED", 0, "boom", h.Now().UTC(), 10))
	got, _ := as.GetJob(ctx, id)
	require.Equal(t, "FAILED", got.Status)
	require.Equal(t, "boom", got.Error)
}

func testASResultsPagination(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
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
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))

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
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
	require.NoError(t, as.DeleteJob(ctx, id))
	_, err := as.GetJob(ctx, id)
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testASReapExpired(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
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
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
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
	require.NoError(t, asA.CreateJob(tenantContext(tA), newSearchJob(h, tA, id)))
	_, err := asB.GetJob(tenantContext(tB), id)
	require.ErrorIs(t, err, spi.ErrNotFound)
}

// findClaimed returns the job with the given ID from a ClaimStale batch, or
// nil if absent. ClaimStale is cross-tenant (search_store.go:121-129) and the
// conformance suite shares one backend across every subtest in the run, so a
// batch may legitimately include stale RUNNING jobs left behind by earlier,
// unrelated subtests. Claim/* tests must look up their own job by ID rather
// than assert an exact batch length or contents.
func findClaimed(batch []*spi.SearchJob, id string) *spi.SearchJob {
	for _, j := range batch {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func testASEpochInitialisedToOne(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	job := newSearchJob(h, tid, id)
	job.Epoch = 42 // CreateJob must ignore this and persist 1 regardless.
	require.NoError(t, as.CreateJob(ctx, job))
	got, err := as.GetJob(ctx, id)
	require.NoError(t, err)
	require.Equal(t, int64(1), got.Epoch)
}

func testASEpochFencedWrites(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))

	// The job's real epoch is 1; every write below is stamped with epoch 2
	// (never claimed) and must be fenced off.
	err := as.UpdateJobStatus(ctx, id, 2, "SUCCESSFUL", 1, "", h.Now(), 5)
	require.ErrorIs(t, err, spi.ErrStaleClaim)

	err = as.Heartbeat(ctx, id, 2)
	require.ErrorIs(t, err, spi.ErrStaleClaim)

	err = as.SaveResults(ctx, id, 2, slices.Values([]string{newID()}))
	require.ErrorIs(t, err, spi.ErrStaleClaim)

	// A fenced write must not partially apply: the job stays RUNNING at
	// epoch 1 with no results persisted.
	got, err := as.GetJob(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", got.Status, "a fenced UpdateJobStatus must not change status")
	require.Equal(t, int64(1), got.Epoch, "a fenced write must not touch Epoch")
	_, total, err := as.GetResultIDs(ctx, id, 0, 10)
	require.NoError(t, err)
	require.Equal(t, 0, total, "a fenced SaveResults must not persist any ids")
}

func testASTerminalWriteOnce(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
	require.NoError(t, as.UpdateJobStatus(ctx, id, 1, "SUCCESSFUL", 5, "", h.Now(), 10))

	err := as.UpdateJobStatus(ctx, id, 1, "FAILED", 0, "boom", h.Now(), 5)
	require.ErrorIs(t, err, spi.ErrAlreadyTerminal)

	err = as.Heartbeat(ctx, id, 1)
	require.ErrorIs(t, err, spi.ErrAlreadyTerminal)

	err = as.SaveResults(ctx, id, 1, slices.Values([]string{newID()}))
	require.ErrorIs(t, err, spi.ErrAlreadyTerminal)

	// Cancel is the sole idempotent-nil exception: it must not error, and
	// must not overwrite the terminal status already recorded.
	require.NoError(t, as.Cancel(ctx, id, h.Now()))

	got, err := as.GetJob(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "SUCCESSFUL", got.Status, "terminal status must not be overwritten by later writes, including Cancel")
	require.Equal(t, 5, got.ResultCount, "rejected writes must not overwrite fields set by the original terminal transition")
}

func testASClaimStaleClaimed(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
	require.NoError(t, as.Heartbeat(ctx, id, 1))

	staleAfter := 10 * time.Millisecond
	h.AdvanceClock(staleAfter + time.Millisecond)

	claimed, err := as.ClaimStale(ctx, staleAfter, 1000)
	require.NoError(t, err)
	job := findClaimed(claimed, id)
	require.NotNil(t, job, "ClaimStale must return the stale job")
	require.Equal(t, int64(2), job.Epoch, "claiming must bump Epoch")
	require.NotNil(t, job.HeartbeatTime, "claiming must stamp a heartbeat")

	got, err := as.GetJob(ctx, id)
	require.NoError(t, err)
	require.Equal(t, int64(2), got.Epoch, "the epoch bump must be persisted, not just reflected in the return value")

	// Fresh heartbeat: an immediate re-claim with the same staleAfter must
	// not reclaim it.
	reclaimed, err := as.ClaimStale(ctx, staleAfter, 1000)
	require.NoError(t, err)
	require.Nil(t, findClaimed(reclaimed, id), "claiming must refresh HeartbeatTime; an immediate re-claim must not reclaim the same job")
}

func testASClaimFreshNotClaimed(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
	require.NoError(t, as.Heartbeat(ctx, id, 1))

	claimed, err := as.ClaimStale(ctx, 10*time.Millisecond, 1000)
	require.NoError(t, err)
	require.Nil(t, findClaimed(claimed, id), "a job heartbeated within staleAfter must not be claimed")
}

func testASClaimNilHeartbeatBaseline(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))

	pre, err := as.GetJob(ctx, id)
	require.NoError(t, err)
	require.Nil(t, pre.HeartbeatTime, "a never-heartbeated job has nil HeartbeatTime")

	staleAfter := 10 * time.Millisecond
	h.AdvanceClock(staleAfter + time.Millisecond)

	claimed, err := as.ClaimStale(ctx, staleAfter, 1000)
	require.NoError(t, err)
	require.NotNil(t, findClaimed(claimed, id), "staleness must fall back to CreateTime when HeartbeatTime is nil")
}

func testASClaimConcurrentDisjoint(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	ids := []string{newID(), newID(), newID()}
	for _, id := range ids {
		require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
	}

	staleAfter := 10 * time.Millisecond
	h.AdvanceClock(staleAfter + time.Millisecond)

	// Two sequential claims stand in for concurrent claimers: since claiming
	// bumps Epoch and refreshes HeartbeatTime, the second call must not
	// re-take anything the first call already took.
	first, err := as.ClaimStale(ctx, staleAfter, 1000)
	require.NoError(t, err)
	for _, id := range ids {
		require.NotNil(t, findClaimed(first, id), "first claim must pick up job %s", id)
	}

	second, err := as.ClaimStale(ctx, staleAfter, 1000)
	require.NoError(t, err)
	for _, id := range ids {
		require.Nil(t, findClaimed(second, id), "second claim must not re-take job %s the first claim already took", id)
	}
}

func testASClaimTerminalNeverClaimed(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
	require.NoError(t, as.UpdateJobStatus(ctx, id, 1, "SUCCESSFUL", 0, "", h.Now(), 0))

	// Arbitrarily stale, to rule out any staleAfter/limit edge case masking
	// a store that claims terminal jobs.
	h.AdvanceClock(365 * 24 * time.Hour)

	claimed, err := as.ClaimStale(ctx, 10*time.Millisecond, 1000)
	require.NoError(t, err)
	require.Nil(t, findClaimed(claimed, id), "a terminal job must never be claimed regardless of staleness")
}

func testASClearResultsIdempotent(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
	ids := []string{newID(), newID(), newID()}
	require.NoError(t, as.SaveResults(ctx, id, 1, slices.Values(ids)))

	require.NoError(t, as.ClearResults(ctx, id))
	_, total, err := as.GetResultIDs(ctx, id, 0, 10)
	require.NoError(t, err)
	require.Equal(t, 0, total, "ClearResults must delete the persisted result ids")

	require.NoError(t, as.ClearResults(ctx, id), "ClearResults on an already-cleared job must be idempotent")
}

// testASSaveResultsChunkSeqContinuity guards against a store that keys
// result rows by (jobID, seq) without accounting for the epoch: reclaiming a
// job and clearing its prior results must leave no trace of the epoch-1
// write for a subsequent epoch-2 write to collide or interleave with.
func testASSaveResultsChunkSeqContinuity(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))

	firstIDs := []string{newID(), newID(), newID()}
	require.NoError(t, as.SaveResults(ctx, id, 1, slices.Values(firstIDs)))

	staleAfter := 10 * time.Millisecond
	h.AdvanceClock(staleAfter + time.Millisecond)
	claimed, err := as.ClaimStale(ctx, staleAfter, 1000)
	require.NoError(t, err)
	job := findClaimed(claimed, id)
	require.NotNil(t, job, "job must be claimed to advance its epoch for this scenario")
	require.Equal(t, int64(2), job.Epoch)

	require.NoError(t, as.ClearResults(ctx, id))

	secondIDs := []string{newID(), newID(), newID(), newID()}
	require.NoError(t, as.SaveResults(ctx, id, 2, slices.Values(secondIDs)))

	page, total, err := as.GetResultIDs(ctx, id, 0, 10)
	require.NoError(t, err)
	require.Equal(t, len(secondIDs), total, "epoch-1 rows must not resurrect after ClearResults")
	require.Equal(t, secondIDs, page, "second save's ids must page back in second-save order, uncontaminated by the epoch-1 write")
}

func testASGetResultIDsDegenerateInputs(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))

	require.NotPanics(t, func() {
		_, _, err := as.GetResultIDs(ctx, id, -1, 10)
		require.Error(t, err, "offset -1 must be rejected, not silently clamped")
	})
	require.NotPanics(t, func() {
		_, _, err := as.GetResultIDs(ctx, id, 0, 0)
		require.Error(t, err, "limit 0 must be rejected")
	})
	require.NotPanics(t, func() {
		_, _, err := as.GetResultIDs(ctx, id, 0, -1)
		require.Error(t, err, "limit -1 must be rejected")
	})
}

func testASGetResultIDsNonTerminalPartial(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
	ids := []string{newID(), newID(), newID(), newID(), newID()}
	require.NoError(t, as.SaveResults(ctx, id, 1, slices.Values(ids)))

	got, err := as.GetJob(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", got.Status, "job must still be RUNNING for this to exercise the partial-read path")

	page, total, err := as.GetResultIDs(ctx, id, 0, 10)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Equal(t, ids, page)
}

func testASUpdateStatusMissingIsNotFound(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	as, _ := h.Factory.AsyncSearchStore(ctx)
	err := as.UpdateJobStatus(ctx, newID(), 1, "SUCCESSFUL", 0, "", h.Now(), 0) // valid UUID, never written
	require.ErrorIs(t, err, spi.ErrNotFound)
}

func testASUpdateStatusZeroFinishTimeAbsent(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)
	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
	require.NoError(t, as.UpdateJobStatus(ctx, id, 1, "FAILED", 0, "boom", time.Time{}, 0))

	got, err := as.GetJob(ctx, id)
	require.NoError(t, err)
	require.Nil(t, got.FinishTime, "a zero finishTime must be stored as absent, not persisted as a real zero-value timestamp")
}

func testASHeartbeatSemantics(t *testing.T, h Harness) {
	tid := h.NewTenant()
	ctx := tenantContext(tid)
	as, _ := h.Factory.AsyncSearchStore(ctx)

	err := as.Heartbeat(ctx, newID(), 1) // valid UUID, never written
	require.ErrorIs(t, err, spi.ErrNotFound, "heartbeat against a missing job")

	id := newID()
	require.NoError(t, as.CreateJob(ctx, newSearchJob(h, tid, id)))
	err = as.Heartbeat(ctx, id, 2) // real epoch is 1
	require.ErrorIs(t, err, spi.ErrStaleClaim, "heartbeat with the wrong epoch")

	require.NoError(t, as.UpdateJobStatus(ctx, id, 1, "SUCCESSFUL", 0, "", h.Now(), 0))
	err = as.Heartbeat(ctx, id, 1)
	require.ErrorIs(t, err, spi.ErrAlreadyTerminal, "heartbeat against a terminal job")
}
