package spitest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// runConsistencyTimeSuite covers TransactionManager.ConsistencyTime.
func runConsistencyTimeSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "AcknowledgedCommitIncluded", testCTAcknowledgedCommitIncluded)
	runSubtest(t, h, tracker, "CrossTenantCommitIncluded", testCTCrossTenantCommitIncluded)
	runSubtest(t, h, tracker, "LaterCommitStampsAbove", testCTLaterCommitStampsAbove)
	runSubtest(t, h, tracker, "Monotonic", testCTMonotonic)
	runSubtest(t, h, tracker, "FinalUnderConcurrentWrites", testCTFinalUnderConcurrentWrites)
	runSubtest(t, h, tracker, "NonTransactionalSaveIncluded", testCTNonTransactionalSaveIncluded)
	runSubtest(t, h, tracker, "CalledInsideTransaction", testCTCalledInsideTransaction)
}

func commitOne(t *testing.T, h Harness, ctx context.Context, model string) (txID, entityID string) {
	t.Helper()
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)
	es, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	entityID = newID()
	_, err = es.Save(txCtx, newEntity(t, model, entityID, map[string]any{"k": 1}))
	require.NoError(t, err)
	require.NoError(t, tm.Commit(txCtx, txID))
	return txID, entityID
}

// ctWriterDeadline bounds how long the writers run so the case's run time
// stays bounded on every backend.
const ctWriterDeadline = 5 * time.Second

// commitOneErr is commitOne for goroutines other than the test's: it returns
// the failure instead of calling FailNow.
func commitOneErr(h Harness, ctx context.Context, model string) error {
	tm, err := h.Factory.TransactionManager(ctx)
	if err != nil {
		return err
	}
	txID, txCtx, err := tm.Begin(ctx)
	if err != nil {
		return err
	}
	es, err := h.Factory.EntityStore(txCtx)
	if err != nil {
		_ = tm.Rollback(txCtx, txID)
		return err
	}
	e := &spi.Entity{
		Meta: spi.EntityMeta{ID: newID(), ModelRef: spi.ModelRef{EntityName: model, ModelVersion: "1"}},
		Data: []byte(`{"k":1}`),
	}
	if _, err := es.Save(txCtx, e); err != nil {
		_ = tm.Rollback(txCtx, txID)
		return err
	}
	return tm.Commit(txCtx, txID)
}

func testCTAcknowledgedCommitIncluded(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, _ := commitOne(t, h, ctx, "m-ct-ack")
	c, err := tm.ConsistencyTime(ctx)
	require.NoError(t, err)
	submit, err := tm.GetSubmitTime(ctx, txID)
	require.NoError(t, err)
	require.False(t, submit.After(c), "acknowledged commit %v must be <= C %v", submit, c)
}

func testCTCrossTenantCommitIncluded(t *testing.T, h Harness) {
	ctxA := tenantContext(h.NewTenant())
	ctxB := tenantContext(h.NewTenant())
	tmA, err := h.Factory.TransactionManager(ctxA)
	require.NoError(t, err)
	tmB, err := h.Factory.TransactionManager(ctxB)
	require.NoError(t, err)
	txID, _ := commitOne(t, h, ctxA, "m-ct-xt")
	cB, err := tmB.ConsistencyTime(ctxB)
	require.NoError(t, err)
	submit, err := tmA.GetSubmitTime(ctxA, txID)
	require.NoError(t, err)
	require.False(t, submit.After(cB), "tenant A's commit %v must be <= tenant B's C %v", submit, cB)
}

func testCTLaterCommitStampsAbove(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	c, err := tm.ConsistencyTime(ctx)
	require.NoError(t, err)
	txID, _ := commitOne(t, h, ctx, "m-ct-later")
	submit, err := tm.GetSubmitTime(ctx, txID)
	require.NoError(t, err)
	require.True(t, submit.After(c), "commit after C must stamp > C (submit %v, C %v)", submit, c)
}

func testCTMonotonic(t *testing.T, h Harness) {
	ctxA := tenantContext(h.NewTenant())
	ctxB := tenantContext(h.NewTenant())
	tmA, err := h.Factory.TransactionManager(ctxA)
	require.NoError(t, err)
	tmB, err := h.Factory.TransactionManager(ctxB)
	require.NoError(t, err)
	var prev time.Time
	for i := 0; i < 20; i++ {
		// A commit in tenant A between calls makes the stamps advance.
		commitOne(t, h, ctxA, "m-ct-mono")
		ctx, tm := ctxA, tmA
		if i%2 == 1 {
			ctx, tm = ctxB, tmB
		}
		c, err := tm.ConsistencyTime(ctx)
		require.NoError(t, err)
		require.False(t, c.Before(prev), "C went backwards: %v after %v", c, prev)
		prev = c
	}
}

// Writers commit concurrently while a checker takes C, counts at C, waits and
// counts again. Both counts must match and must include every commit
// acknowledged before the request. Deterministic proof of the wait lives in
// each plugin's white-box tests; this case catches an implementation that
// returns the raw clock.
func testCTFinalUnderConcurrentWrites(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	mref := spi.ModelRef{EntityName: "m-ct-final", ModelVersion: "1"}
	var acked atomic.Int64
	stop := make(chan struct{})
	errCh := make(chan error, 4)
	var wg sync.WaitGroup
	var once sync.Once
	halt := func() {
		once.Do(func() { close(stop) })
		wg.Wait()
	}
	defer halt()
	deadline := time.Now().Add(ctWriterDeadline)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				select {
				case <-stop:
					return
				default:
				}
				if err := commitOneErr(h, ctx, "m-ct-final"); err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
				acked.Add(1)
			}
		}()
	}
	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	var ackedAtFirst, ackedAtLast int64
	for i := 0; i < 20; i++ {
		before := acked.Load()
		if i == 0 {
			ackedAtFirst = before
		}
		if i == 19 {
			ackedAtLast = before
		}
		c, err := tm.ConsistencyTime(ctx)
		require.NoError(t, err)
		n1, err := es.Count(ctx, mref, &c)
		require.NoError(t, err)
		select {
		case werr := <-errCh:
			halt()
			require.NoError(t, werr, "writer failed")
		default:
		}
		require.GreaterOrEqual(t, n1, before, "count at C must include every acknowledged commit")
		h.AdvanceClock(5 * time.Millisecond)
		n2, err := es.Count(ctx, mref, &c)
		require.NoError(t, err)
		require.Equal(t, n1, n2, "count at C changed after it was given")
	}
	halt()
	select {
	case werr := <-errCh:
		require.NoError(t, werr, "writer failed")
	default:
	}
	require.Greater(t, ackedAtLast, ackedAtFirst,
		"no commit was acknowledged while the checks ran: the writers finished early and the case proved nothing")
}

func testCTNonTransactionalSaveIncluded(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	es, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	id := newID()
	_, err = es.Save(ctx, newEntity(t, "m-ct-nontx", id, map[string]any{"k": 1}))
	require.NoError(t, err)
	c, err := tm.ConsistencyTime(ctx)
	require.NoError(t, err)
	got, err := es.GetAsAt(ctx, id, c)
	require.NoError(t, err)
	require.Equal(t, id, got.Meta.ID)
}

func testCTCalledInsideTransaction(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)
	es, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	_, err = es.Save(txCtx, newEntity(t, "m-ct-intx", newID(), map[string]any{"k": 1}))
	require.NoError(t, err)

	// A separate transaction commits while T is open.
	h.AdvanceClock(10 * time.Millisecond)
	xID, _ := commitOne(t, h, ctx, "m-ct-intx-other")

	// Bounded so a backend that waits on T fails instead of hanging the suite.
	callCtx, cancel := context.WithTimeout(txCtx, 30*time.Second)
	defer cancel()
	c, err := tm.ConsistencyTime(callCtx)
	require.NoError(t, err)
	submit, err := tm.GetSubmitTime(ctx, xID)
	require.NoError(t, err)
	require.False(t, submit.After(c), "commit %v acknowledged before the call must be <= C %v", submit, c)

	// The transaction is still usable and still ours to commit.
	require.NoError(t, tm.Commit(txCtx, txID))
}
