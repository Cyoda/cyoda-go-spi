package spitest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// beginGuarded begins a transaction and registers a t.Cleanup that
// rolls it back unconditionally. It exists because tx subtests need
// direct Begin/Commit/Rollback control (unlike withTx) to inspect
// in-flight state, but a require.* failure between Begin and the
// test's own Commit/Rollback would otherwise leave the transaction
// active for the rest of the run — on backends whose committed-log
// pruning requires zero active transactions (e.g. sqlite), one leaked
// tx cascades into unrelated subtest failures.
//
// The registered Rollback is purely a safety net: on the happy path
// the test still calls Commit/Rollback itself, and the cleanup's
// resulting error (tx already terminated) is ignored. Callers must not
// rely on the guard for actual test semantics.
func beginGuarded(t *testing.T, tm spi.TransactionManager, ctx spiCtx) (txID string, txCtx spiCtx) {
	t.Helper()
	txID, txCtx, err := tm.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tm.Rollback(txCtx, txID) })
	return txID, txCtx
}

// runTransactionSuite covers TransactionManager. Each subtest gets a
// fresh tenant.
func runTransactionSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "CommitVisibility", testTxCommitVisibility)
	runSubtest(t, h, tracker, "RollbackDiscards", testTxRollbackDiscards)
	runSubtest(t, h, tracker, "Join", testTxJoin)
	runSubtest(t, h, tracker, "SubmitTime", testTxSubmitTime)
	runSubtest(t, h, tracker, "Savepoint/ReleaseMergesWork", testTxSavepointRelease)
	runSubtest(t, h, tracker, "Savepoint/RollbackToDiscards", testTxSavepointRollback)
	runSubtest(t, h, tracker, "BeginAfterCommit", testTxBeginAfterCommit)
	runSubtest(t, h, tracker, "TxStateErrors/JoinAfterCommit", testTxStateJoinAfterCommit)
	runSubtest(t, h, tracker, "TxStateErrors/CommitAfterCommit", testTxStateCommitAfterCommit)
	runSubtest(t, h, tracker, "TxStateErrors/CommitAfterRollback", testTxStateCommitAfterRollback)
	runSubtest(t, h, tracker, "TxStateErrors/OpAfterRollback", testTxStateOpAfterRollback)
	runSubtest(t, h, tracker, "TxStateErrors/TenantMismatchOnJoin", testTxStateTenantMismatchOnJoin)
	runSubtest(t, h, tracker, "TxStateErrors/TenantMismatchOnCommit", testTxStateTenantMismatchOnCommit)
	runSubtest(t, h, tracker, "TxStateErrors/SavepointNotFound", testTxStateSavepointNotFound)
	runSubtest(t, h, tracker, "Attribution/OriginCaptureAndJoin", testTxOriginCaptureAndJoin)
	runSubtest(t, h, tracker, "Attribution/OriginAmbientRoot", testTxOriginAmbientRoot)
	runSubtest(t, h, tracker, "Attribution/DeleteAttributionSavepoint", testTxDeleteAttributionSavepoint)
}

// Writes in an open tx are invisible to outside readers; after Commit
// they are visible.
func testTxCommitVisibility(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	txID, txCtx := beginGuarded(t, tm, ctx)

	es, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)

	id := newID()
	ent := newEntity(t, "m-commit", id, map[string]any{"k": "v"})
	_, err = es.Save(txCtx, ent)
	require.NoError(t, err)

	// Outside-tx read must not see the write yet.
	esOutside, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	_, err = esOutside.Get(ctx, id)
	require.ErrorIs(t, err, spi.ErrNotFound, "outside reader must not see uncommitted write")

	// Use txCtx (not ctx) so backends that store tx-state in the context
	// (e.g. Cassandra) can locate the transaction on Commit.
	require.NoError(t, tm.Commit(txCtx, txID))

	got, err := esOutside.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, id, got.Meta.ID)
}

func testTxRollbackDiscards(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	id := newID()
	txID, txCtx := beginGuarded(t, tm, ctx)
	es, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	_, err = es.Save(txCtx, newEntity(t, "m-rb", id, map[string]any{"k": 1}))
	require.NoError(t, err)

	// Use txCtx (not ctx) so backends that embed tx-state in the context
	// (e.g. Cassandra) can locate the transaction on Rollback.
	require.NoError(t, tm.Rollback(txCtx, txID))

	esOutside, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	_, err = esOutside.Get(ctx, id)
	require.ErrorIs(t, err, spi.ErrNotFound, "rolled-back write must never be visible")
}

func testTxJoin(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	id := newID()
	txID, txCtx1 := beginGuarded(t, tm, ctx)
	es1, err := h.Factory.EntityStore(txCtx1)
	require.NoError(t, err)
	_, err = es1.Save(txCtx1, newEntity(t, "m-join", id, map[string]any{"side": "A"}))
	require.NoError(t, err)

	txCtx2, err := tm.Join(ctx, txID)
	require.NoError(t, err)
	es2, err := h.Factory.EntityStore(txCtx2)
	require.NoError(t, err)
	got, err := es2.Get(txCtx2, id)
	require.NoError(t, err)
	require.Equal(t, id, got.Meta.ID, "second caller on same tx must see first caller's uncommitted write")

	// Use txCtx1 (not ctx) so backends that embed tx-state in the context
	// (e.g. Cassandra) can locate the transaction on Rollback.
	require.NoError(t, tm.Rollback(txCtx1, txID))
}

func testTxSubmitTime(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	before := h.Now().UTC()
	txID, txCtx := beginGuarded(t, tm, ctx)
	// Pass txCtx (not ctx) so backends that store tx-state in the context
	// (e.g. Cassandra) can locate the transaction on Commit.
	require.NoError(t, tm.Commit(txCtx, txID))
	after := h.Now().UTC()

	submit, err := tm.GetSubmitTime(ctx, txID)
	require.NoError(t, err)
	require.False(t, submit.Before(before.Add(-5*time.Millisecond)),
		"submit time %v must not precede Begin (before=%v)", submit, before)
	require.False(t, submit.After(after.Add(5*time.Millisecond)),
		"submit time %v must not follow Commit-return (after=%v)", submit, after)
}

func testTxSavepointRelease(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	idPre := newID()
	idPost := newID()

	txID, txCtx := beginGuarded(t, tm, ctx)
	es, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	_, err = es.Save(txCtx, newEntity(t, "m-sp", idPre, map[string]any{}))
	require.NoError(t, err)

	// Use txCtx for all TM calls after Begin: Cassandra embeds tx-state in
	// the context and requires it for Savepoint, ReleaseSavepoint, and Commit.
	sp, err := tm.Savepoint(txCtx, txID)
	require.NoError(t, err)
	// After Savepoint, txCtx is replaced with the new savepoint context.
	// Save subsequent entities via the original es (which was created from
	// the original txCtx); further saves after Savepoint still use txCtx.
	_, err = es.Save(txCtx, newEntity(t, "m-sp", idPost, map[string]any{}))
	require.NoError(t, err)

	require.NoError(t, tm.ReleaseSavepoint(txCtx, txID, sp))
	require.NoError(t, tm.Commit(txCtx, txID))

	esOut, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	_, err = esOut.Get(ctx, idPre)
	require.NoError(t, err, "pre-savepoint write must survive release")
	_, err = esOut.Get(ctx, idPost)
	require.NoError(t, err, "post-savepoint write must survive release")
}

func testTxSavepointRollback(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	idPre := newID()
	idPost := newID()

	txID, txCtx := beginGuarded(t, tm, ctx)
	es, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	_, err = es.Save(txCtx, newEntity(t, "m-sp", idPre, map[string]any{}))
	require.NoError(t, err)

	// Use txCtx for all TM calls after Begin: Cassandra embeds tx-state in
	// the context and requires it for Savepoint, RollbackToSavepoint, and Commit.
	sp, err := tm.Savepoint(txCtx, txID)
	require.NoError(t, err)
	_, err = es.Save(txCtx, newEntity(t, "m-sp", idPost, map[string]any{}))
	require.NoError(t, err)

	require.NoError(t, tm.RollbackToSavepoint(txCtx, txID, sp))
	require.NoError(t, tm.Commit(txCtx, txID))

	esOut, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	_, err = esOut.Get(ctx, idPre)
	require.NoError(t, err, "pre-savepoint write must survive rollback-to-savepoint")
	_, err = esOut.Get(ctx, idPost)
	require.ErrorIs(t, err, spi.ErrNotFound, "post-savepoint write must be discarded")
}

func testTxBeginAfterCommit(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	txID, txCtx := beginGuarded(t, tm, ctx)
	// Pass txCtx (not ctx) so backends that store tx-state in the context
	// (e.g. Cassandra) can locate the transaction on Commit.
	require.NoError(t, tm.Commit(txCtx, txID))

	// Kept as a loose-assertion floor: any error suffices. The strict
	// version that asserts errors.Is(err, spi.ErrTxAlreadyCommitted) (and
	// the ErrTxTerminated parent via Unwrap) lives in TxStateErrors/
	// JoinAfterCommit. Both coexist intentionally: this one runs against
	// backends that haven't yet conformed to the sentinel contract.
	// TODO(retire-when-all-backends-conform): drop this subtest once every
	// known consumer asserts the strict TxStateErrors/JoinAfterCommit path.
	_, err = tm.Join(ctx, txID)
	require.Error(t, err, "Join against committed txID must fail")
}

// testTxStateJoinAfterCommit verifies that joining a transaction whose
// terminal state is Commit produces ErrTxAlreadyCommitted (which also
// matches ErrTxTerminated via Unwrap).
func testTxStateJoinAfterCommit(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	txID, txCtx := beginGuarded(t, tm, ctx)
	require.NoError(t, tm.Commit(txCtx, txID))

	_, err = tm.Join(ctx, txID)
	require.Error(t, err, "Join after Commit must fail")
	require.True(t,
		errors.Is(err, spi.ErrTxAlreadyCommitted) || errors.Is(err, spi.ErrTxNotFound),
		"Join after Commit must wrap ErrTxAlreadyCommitted or ErrTxNotFound (backends that purge committed-tx state collapse these); got: %v", err)
}

// testTxStateCommitAfterCommit verifies that double-Commit produces
// ErrTxAlreadyCommitted or ErrTxNotFound (backends that purge state
// after the first Commit collapse to NotFound).
func testTxStateCommitAfterCommit(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	txID, txCtx := beginGuarded(t, tm, ctx)
	require.NoError(t, tm.Commit(txCtx, txID))

	err = tm.Commit(txCtx, txID)
	require.Error(t, err, "second Commit must fail")
	require.True(t,
		errors.Is(err, spi.ErrTxAlreadyCommitted) || errors.Is(err, spi.ErrTxNotFound),
		"second Commit must wrap ErrTxAlreadyCommitted or ErrTxNotFound; got: %v", err)
}

// testTxStateCommitAfterRollback verifies that Commit on a rolled-back tx
// produces ErrTxRolledBack or ErrTxNotFound (backends that purge state
// after Rollback collapse to NotFound).
func testTxStateCommitAfterRollback(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	txID, txCtx := beginGuarded(t, tm, ctx)
	require.NoError(t, tm.Rollback(txCtx, txID))

	err = tm.Commit(txCtx, txID)
	require.Error(t, err, "Commit after Rollback must fail")
	require.True(t,
		errors.Is(err, spi.ErrTxRolledBack) || errors.Is(err, spi.ErrTxNotFound),
		"Commit after Rollback must wrap ErrTxRolledBack or ErrTxNotFound; got: %v", err)
}

// testTxStateOpAfterRollback verifies that a data op against a rolled-back
// transaction produces ErrTxTerminated. Backends that delegate transaction
// state to an external engine may skip this via Harness.Skip — see the
// ErrTxTerminated godoc caveat.
func testTxStateOpAfterRollback(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	txID, txCtx := beginGuarded(t, tm, ctx)

	es, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)

	id := newID()
	_, err = es.Save(txCtx, newEntity(t, "m-op-after-rb", id, map[string]any{"k": "v"}))
	require.NoError(t, err)

	require.NoError(t, tm.Rollback(txCtx, txID))

	_, err = es.Get(txCtx, id)
	require.Error(t, err, "Get after Rollback must fail")
	require.True(t, errors.Is(err, spi.ErrTxTerminated),
		"op after Rollback must wrap ErrTxTerminated; got: %v", err)
}

// testTxStateTenantMismatchOnJoin verifies that tenant B cannot Join a
// transaction begun by tenant A; the error wraps ErrTxTenantMismatch.
func testTxStateTenantMismatchOnJoin(t *testing.T, h Harness) {
	ctxA := tenantContext(h.NewTenant())
	ctxB := tenantContext(h.NewTenant())

	tmA, err := h.Factory.TransactionManager(ctxA)
	require.NoError(t, err)
	txID, _ := beginGuarded(t, tmA, ctxA)

	tmB, err := h.Factory.TransactionManager(ctxB)
	require.NoError(t, err)
	_, err = tmB.Join(ctxB, txID)
	require.Error(t, err, "tenant B Join of tenant A tx must fail")
	require.True(t, errors.Is(err, spi.ErrTxTenantMismatch),
		"cross-tenant Join must wrap ErrTxTenantMismatch; got: %v", err)
}

// testTxStateTenantMismatchOnCommit verifies that tenant B cannot Commit a
// transaction begun by tenant A; the error wraps ErrTxTenantMismatch.
func testTxStateTenantMismatchOnCommit(t *testing.T, h Harness) {
	ctxA := tenantContext(h.NewTenant())
	ctxB := tenantContext(h.NewTenant())

	tmA, err := h.Factory.TransactionManager(ctxA)
	require.NoError(t, err)
	txID, _ := beginGuarded(t, tmA, ctxA)

	tmB, err := h.Factory.TransactionManager(ctxB)
	require.NoError(t, err)
	err = tmB.Commit(ctxB, txID)
	require.Error(t, err, "tenant B Commit of tenant A tx must fail")
	require.True(t, errors.Is(err, spi.ErrTxTenantMismatch),
		"cross-tenant Commit must wrap ErrTxTenantMismatch; got: %v", err)
}

// testTxStateSavepointNotFound verifies that RollbackToSavepoint with an
// unknown savepoint id produces ErrSavepointNotFound (which also matches
// ErrNotFound via Unwrap).
func testTxStateSavepointNotFound(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)

	txID, txCtx := beginGuarded(t, tm, ctx)

	err = tm.RollbackToSavepoint(txCtx, txID, "no-such-savepoint")
	require.Error(t, err, "RollbackToSavepoint with unknown id must fail")
	require.True(t, errors.Is(err, spi.ErrSavepointNotFound),
		"unknown savepoint must wrap ErrSavepointNotFound; got: %v", err)
	require.True(t, errors.Is(err, spi.ErrNotFound),
		"ErrSavepointNotFound must also match ErrNotFound via Unwrap; got: %v", err)
}

// testTxOriginCaptureAndJoin verifies that Begin captures the
// UserContext-derived Principal as TransactionState.Origin, and that Join
// from a second, same-tenant but different-kind context does not disturb
// it — Origin is the immutable attribution root for the whole tx, not a
// per-caller value (see ResolveOrigin precedence and the Origin godoc in
// TransactionState).
func testTxOriginCaptureAndJoin(t *testing.T, h Harness) {
	tenant := h.NewTenant()
	rootCtx := tenantContextAs(tenant, "root-user", spi.PrincipalUser)

	tm, err := h.Factory.TransactionManager(rootCtx)
	require.NoError(t, err)
	txID, txCtx1 := beginGuarded(t, tm, rootCtx)

	tx1 := spi.GetTransaction(txCtx1)
	require.NotNil(t, tx1, "Begin must populate TransactionState in the returned context")
	require.Equal(t, spi.Principal{ID: "root-user", Kind: spi.PrincipalUser}, tx1.Origin,
		"Origin must capture the Begin caller's UserContext-derived Principal")

	// Join from a second context: same tenant, different (service-kind) actor.
	joinCtx := tenantContextAs(tenant, "joiner-svc", spi.PrincipalService)
	txCtx2, err := tm.Join(joinCtx, txID)
	require.NoError(t, err)

	tx2 := spi.GetTransaction(txCtx2)
	require.NotNil(t, tx2, "Join must populate TransactionState in the returned context")
	require.Equal(t, tx1.Origin, tx2.Origin,
		"Join must not overwrite Origin with the joiner's own Principal — postgres-style rebuilds must repopulate it")
	require.Equal(t, spi.Principal{ID: "root-user", Kind: spi.PrincipalUser}, tx2.Origin)
}

// testTxOriginAmbientRoot verifies that Begin with no parent transaction but
// an ambient origin seeded via WithAmbientOrigin uses that seed as Origin,
// taking precedence over the caller's UserContext-derived Principal — the
// scheduled-fire case, per ResolveOrigin's documented precedence
// (parent-tx > ambient > UserContext).
func testTxOriginAmbientRoot(t *testing.T, h Harness) {
	tenant := h.NewTenant()
	base := tenantContextAs(tenant, "ambient-caller", spi.PrincipalUser)
	seed := spi.Principal{ID: "scheduler", Kind: spi.PrincipalSystem}
	ctx := spi.WithAmbientOrigin(base, seed)

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	_, txCtx := beginGuarded(t, tm, ctx)

	tx := spi.GetTransaction(txCtx)
	require.NotNil(t, tx, "Begin must populate TransactionState in the returned context")
	require.Equal(t, seed, tx.Origin,
		"ambient origin must win over the caller's UserContext-derived Principal when no parent tx exists")
}

// testTxDeleteAttributionSavepoint verifies DeleteAttribution stages
// alongside Deletes, is restored 1:1 with Deletes across
// RollbackToSavepoint, and that the surviving entry's attribution is what
// the committed tombstone carries.
func testTxDeleteAttributionSavepoint(t *testing.T, h Harness) {
	tenant := h.NewTenant()
	ctx := tenantContextAs(tenant, "actor-1", spi.PrincipalUser)
	idA := newID()
	idB := newID()

	withTx(t, h, ctx, func(txCtx context.Context) {
		es, err := h.Factory.EntityStore(txCtx)
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-delattr", idA, map[string]any{}))
		require.NoError(t, err)
		_, err = es.Save(txCtx, newEntity(t, "m-delattr", idB, map[string]any{}))
		require.NoError(t, err)
	})

	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx := beginGuarded(t, tm, ctx)

	es, err := h.Factory.EntityStore(txCtx)
	require.NoError(t, err)
	require.NoError(t, es.Delete(txCtx, idA))

	tx := spi.GetTransaction(txCtx)
	require.NotNil(t, tx)
	stagedA, ok := tx.DeleteAttribution[idA]
	require.True(t, ok, "DeleteAttribution must record A's delete as soon as it is staged")
	wantA := spi.Principal{ID: "actor-1", Kind: spi.PrincipalUser}
	require.Equal(t, wantA, stagedA.Attributed)
	require.Equal(t, wantA, stagedA.Executor)

	sp, err := tm.Savepoint(txCtx, txID)
	require.NoError(t, err)

	require.NoError(t, es.Delete(txCtx, idB))
	tx = spi.GetTransaction(txCtx)
	require.Len(t, tx.Deletes, 2, "both deletes staged before RollbackToSavepoint")
	require.Len(t, tx.DeleteAttribution, 2, "DeleteAttribution must track every staged delete, 1:1 with Deletes")

	require.NoError(t, tm.RollbackToSavepoint(txCtx, txID, sp))

	tx = spi.GetTransaction(txCtx)
	require.Equal(t, map[string]bool{idA: true}, tx.Deletes,
		"RollbackToSavepoint must restore Deletes to exactly the pre-savepoint set")
	require.Len(t, tx.DeleteAttribution, 1,
		"DeleteAttribution must be restored in lockstep with Deletes — same key set, always")
	require.Equal(t, stagedA, tx.DeleteAttribution[idA],
		"A's attribution must be unchanged by the savepoint round-trip")
	_, bStillPresent := tx.DeleteAttribution[idB]
	require.False(t, bStillPresent, "B's attribution must not survive rollback-to-savepoint")

	require.NoError(t, tm.Commit(txCtx, txID))

	esOut, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	history, err := esOut.GetVersionHistory(ctx, idA)
	require.NoError(t, err)
	require.NotEmpty(t, history)
	tombstone := history[len(history)-1]
	require.True(t, tombstone.Deleted, "A's last version must be the committed tombstone")
	require.Equal(t, wantA.Kind, tombstone.AttributedKind,
		"tombstone's AttributedKind must match the staged DeleteAttribution")
	require.Equal(t, wantA, tombstone.Executor,
		"tombstone's Executor must match the staged DeleteAttribution")

	esB, err := h.Factory.EntityStore(ctx)
	require.NoError(t, err)
	_, err = esB.Get(ctx, idB)
	require.NoError(t, err, "B must survive commit — its delete was discarded by RollbackToSavepoint")
}
