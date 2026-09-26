package spitest

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// A task-row write that a savepoint rollback discards follows the same rule
// as an entity write (see the Transaction/Savepoint cases): if another
// transaction committed a change to that row after the transaction's
// snapshot and before the rollback, the write has lost first-committer-wins,
// the rollback does not undo that, and Commit refuses the transaction with
// ErrConflict. A change committed after the rollback raced no write of the
// transaction and does not conflict.

// stSavepointWrites are the joining writes a callback makes to an entity's
// task rows when it updates or deletes the entity.
var stSavepointWrites = []struct {
	name  string
	write func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error
}{
	{"DeleteForEntities", func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
		return f.sts.DeleteForEntities(ctx, f.tenant, []string{c.EntityID})
	}},
	{"ReconcileForEntity", func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error {
		_, err := f.sts.ReconcileForEntity(ctx, spi.ReconcileRequest{TenantID: f.tenant, EntityID: c.EntityID,
			CurrentState: "S", Arm: []spi.ScheduledTask{f.spec(c.EntityID, "S", "T", stFuture)}})
		return err
	}},
}

// stSavepointRace is one savepoint case: a committed due task the race is
// about, and an open transaction that has already armed a task of another
// entity before any savepoint, so every backend has taken its snapshot.
type stSavepointRace struct {
	f      *stFixture
	target spi.ScheduledTask
	pre    spi.ScheduledTask // armed by the transaction before the savepoint
	txID   string
	txCtx  context.Context
	sp     string
}

func newSTSavepointRace(t *testing.T, h Harness) *stSavepointRace {
	t.Helper()
	f := newSTFixture(t, h)
	r := &stSavepointRace{f: f, target: f.armDue()}
	r.txID, r.txCtx = f.begin()
	e := f.newEntity()
	r.pre = f.spec(e, "S", "T", stFuture)
	f.reconcile(r.txCtx, e, "S", r.pre)
	var err error
	r.sp, err = f.tm.Savepoint(r.txCtx, r.txID)
	require.NoError(t, err)
	return r
}

// write makes the transaction's task-row write to the target. A backend that
// detects the conflict at the write refuses it with ErrConflict; one that
// detects it at commit accepts it. Both are conforming.
func (r *stSavepointRace) write(t *testing.T, w func(ctx context.Context, f *stFixture, c spi.ScheduledTask) error) {
	t.Helper()
	if err := w(r.txCtx, r.f, r.target); err != nil {
		require.ErrorIs(t, err, spi.ErrConflict, "a task-row write that loses the race may only be refused as a conflict")
	}
}

func (r *stSavepointRace) rollback(t *testing.T) {
	t.Helper()
	require.NoError(t, r.f.tm.RollbackToSavepoint(r.txCtx, r.txID, r.sp))
}

// The scheduler claims the target after the transaction's snapshot; the
// transaction then writes the target's row inside a savepoint and rolls the
// savepoint back. The discarded write lost the race: Commit refuses the
// transaction, nothing it wrote is applied, and the claim stands.
func testSTSavepointRollbackKeepsLostRace(t *testing.T, h Harness) {
	for _, w := range stSavepointWrites {
		t.Run(w.name, func(t *testing.T) {
			r := newSTSavepointRace(t, h)
			c := r.f.claimTask(uuid.New(), r.target.ID)
			r.write(t, w.write)
			r.rollback(t)
			require.ErrorIs(t, r.f.tm.Commit(r.txCtx, r.txID), spi.ErrConflict,
				"a task-row write that lost a race stays lost when a savepoint rollback discards it: Commit must refuse the transaction")
			r.f.requireGone(r.pre.ID)
			requireUnchangedClaim(t, c, r.f.mustGet(r.target.ID))
		})
	}
}

// Control: the claim commits after the savepoint rollback discarded the
// transaction's write, so it raced no write of the transaction and Commit
// succeeds.
func testSTSavepointRollbackDiscardedRivalAfter(t *testing.T, h Harness) {
	for _, w := range stSavepointWrites {
		t.Run(w.name, func(t *testing.T) {
			r := newSTSavepointRace(t, h)
			r.write(t, w.write)
			r.rollback(t)
			c := r.f.claimTask(uuid.New(), r.target.ID)
			require.NoError(t, r.f.tm.Commit(r.txCtx, r.txID),
				"a claim committed after the rollback must not make Commit conflict")
			requireFreshLife(t, r.pre, r.f.mustGet(r.pre.ID))
			requireUnchangedClaim(t, c, r.f.mustGet(r.target.ID))
		})
	}
}

// Control: a discarded task-row write that no one raced does not conflict,
// and the row is as it was before the transaction.
func testSTSavepointRollbackDiscardedNoRival(t *testing.T, h Harness) {
	for _, w := range stSavepointWrites {
		t.Run(w.name, func(t *testing.T) {
			r := newSTSavepointRace(t, h)
			r.write(t, w.write)
			r.rollback(t)
			require.NoError(t, r.f.tm.Commit(r.txCtx, r.txID),
				"a discarded task-row write that no one raced must not make Commit conflict")
			requireFreshLife(t, r.pre, r.f.mustGet(r.pre.ID))
			got := r.f.mustGet(r.target.ID)
			require.Equal(t, r.target.ArmToken, got.ArmToken, "the discarded write must not apply")
			require.Equal(t, spi.ScheduledTaskWaiting, got.Status)
		})
	}
}
