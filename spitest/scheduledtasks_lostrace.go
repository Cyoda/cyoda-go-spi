package spitest

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The ScheduledTasks LostRace cases pin TransactionManager.LostRace for task
// rows, as the Transaction LostRace cases pin it for entities: a transaction
// whose joining write meets a task row the scheduler claimed after the
// transaction's snapshot has lost the race, before it commits, on every
// backend. The fixture is stSavepointRace's; its savepoint is used only
// where a case says so.

// The claim commits after the snapshot; the transaction then writes the row.
func testSTLostRaceWriteLost(t *testing.T, h Harness) {
	for _, w := range stSavepointWrites {
		t.Run(w.name, func(t *testing.T) {
			r := newSTSavepointRace(t, h)
			c := r.f.claimTask(uuid.New(), r.target.ID)
			r.write(t, w.write)
			requireLostRace(t, r.f.tm, r.txCtx, r.txID, true,
				"a task-row write that meets a claim committed after the snapshot has lost the race")
			r.requireLostRaceRefused(t, c)
		})
	}
}

// Control: no claim. LostRace answers false and Commit applies.
func testSTLostRaceNoRival(t *testing.T, h Harness) {
	for _, w := range stSavepointWrites {
		t.Run(w.name, func(t *testing.T) {
			r := newSTSavepointRace(t, h)
			r.write(t, w.write)
			requireLostRace(t, r.f.tm, r.txCtx, r.txID, false, "a task-row write no one raced has not lost")
			require.NoError(t, r.f.tm.Commit(r.txCtx, r.txID))
		})
	}
}

// Control: the claim committed before the transaction began, so the
// transaction's write raced nothing.
func testSTLostRaceClaimBeforeBegin(t *testing.T, h Harness) {
	for _, w := range stSavepointWrites {
		t.Run(w.name, func(t *testing.T) {
			f := newSTFixture(t, h)
			target := f.armDue()
			f.claimTask(uuid.New(), target.ID)
			txID, txCtx := f.begin()
			require.NoError(t, w.write(txCtx, f, target))
			requireLostRace(t, f.tm, txCtx, txID, false, "a claim committed before the snapshot raced nothing")
			require.NoError(t, f.tm.Commit(txCtx, txID))
		})
	}
}

// A savepoint rollback that discards the lost task-row write keeps the loss.
func testSTLostRaceAfterSavepointRollback(t *testing.T, h Harness) {
	for _, w := range stSavepointWrites {
		t.Run(w.name, func(t *testing.T) {
			r := newSTSavepointRace(t, h)
			c := r.f.claimTask(uuid.New(), r.target.ID)
			r.write(t, w.write)
			r.rollback(t)
			requireLostRace(t, r.f.tm, r.txCtx, r.txID, true, "a savepoint rollback does not undo a lost race")
			r.requireLostRaceRefused(t, c)
		})
	}
}

// Control: a claim committed after the savepoint rollback discarded the
// write raced no write of the transaction.
func testSTLostRaceDiscardedRivalAfter(t *testing.T, h Harness) {
	for _, w := range stSavepointWrites {
		t.Run(w.name, func(t *testing.T) {
			r := newSTSavepointRace(t, h)
			r.write(t, w.write)
			r.rollback(t)
			r.f.claimTask(uuid.New(), r.target.ID)
			requireLostRace(t, r.f.tm, r.txCtx, r.txID, false,
				"a claim committed after the rollback raced no write of the transaction")
			require.NoError(t, r.f.tm.Commit(r.txCtx, r.txID))
		})
	}
}
