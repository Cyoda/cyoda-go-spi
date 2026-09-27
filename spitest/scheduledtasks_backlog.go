package spitest

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// A store may bound the work of one claim by looking, per tenant, at no more
// of its due tasks than the claim can take. These cases pin the selection
// such a cut must still produce: every one of them has a claimable task the
// cut must reach.

// stBacklog is the number of due tasks the backlog cases arm for one tenant.
const stBacklog = 2000

// armBacklog arms n due tasks, each on its own entity, NextAttemptTime
// stDue-n .. stDue-1, and returns their ids in that order.
func (f *stFixture) armBacklog(n int) []string {
	f.t.Helper()
	ids := make([]string, n)
	for i := range n {
		e := f.newEntity()
		s := f.spec(e, "S", "T", stDue-int64(n)+int64(i))
		f.reconcile(f.ctx, e, "S", s)
		ids[i] = s.ID
	}
	return ids
}

// Tenant A has a large due backlog, all of it older than tenant B's one task.
// B's task is claimed in the first claim, although A alone could fill the
// Limit; A's tasks are then claimed, PerTenantLimit a round, oldest first,
// until none is left: none is passed over.
func testSTClaimBacklogTenantNotStarved(t *testing.T, h Harness) {
	fa := newSTFixture(t, h)
	fb := newSTFixture(t, h)
	backlog := fa.armBacklog(stBacklog)
	bTask := fb.arm(stDue)

	const perRound = 50
	req := fa.claimReq(uuid.New(), false)
	req.Limit = perRound
	req.PerTenantLimit = perRound

	res, err := fa.sts.ClaimDue(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, []string{bTask.ID}, stIDs(fb.own(res)),
		"B's task is claimed in the first claim, however large A's backlog")
	require.Equal(t, backlog[:perRound-1], stIDs(fa.own(res)),
		"A takes the rest of the Limit, oldest first")

	claimed := perRound - 1
	for claimed < len(backlog) {
		got := stIDs(fa.claimWith(req))
		want := backlog[claimed:min(claimed+perRound, len(backlog))]
		require.Equal(t, want, got, "round after %d claims: A's next oldest tasks", claimed)
		claimed += len(got)
	}
	require.Empty(t, fa.claimWith(req), "every task of A's backlog was claimed once")
}

// One entity has many due tasks, all older than any other entity's. A claim
// takes that entity's oldest task, then fills the tenant's quota from the
// next entities: the entity's other tasks do not take the tenant's turns.
func testSTClaimBacklogEntityDoesNotFillQuota(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	x := f.newEntity()
	var specs []spi.ScheduledTask
	for i := range 20 {
		// The oldest task has the largest id: the entity's task is chosen by
		// NextAttemptTime, not by id.
		specs = append(specs, f.spec(x, "S", fmt.Sprintf("T%02d", i), stDue-100+int64(19-i)))
	}
	f.reconcile(f.ctx, x, "S", specs...)
	y := f.arm(stDue - 50)
	z := f.arm(stDue - 40)
	f.arm(stDue - 30)

	req := f.claimReq(uuid.New(), false)
	req.PerTenantLimit = 3
	require.Equal(t, []string{specs[19].ID, y.ID, z.ID}, stIDs(f.claimWith(req)),
		"one task of the busy entity, its oldest, then the next entities fill the quota")
}

// An entity's oldest task is busy under an open transaction (C6). Its turn
// passes to the entity's next task, which is later than another entity's
// task: the claim takes both, the busy task's sibling included.
func testSTClaimBacklogBusyOldestSiblingPassesOn(t *testing.T, h Harness) {
	f := newSTFixture(t, h)
	x := f.newEntity()
	x1 := f.spec(x, "S", "T1", stDue-100)
	x2 := f.spec(x, "S", "T2", stDue-50)
	f.reconcile(f.ctx, x, "S", x1, x2)
	y := f.arm(stDue - 80)
	f.arm(stDue - 10)

	txID, txCtx := f.begin()
	require.NoError(t, f.sts.RemoveLife(txCtx, f.tenant, x1.ID, f.mustGet(x1.ID).ArmToken))

	req := f.claimReq(uuid.New(), false)
	req.PerTenantLimit = 2
	require.ElementsMatch(t, []string{y.ID, x2.ID}, stIDs(f.claimWith(req)),
		"the busy task's turn passes to its sibling, which fills the quota before a later entity")

	require.NoError(t, f.tm.Rollback(txCtx, txID))
}

// A lost-owner RUNNING task and the due WAITING tasks share their tenant's
// quota in NextAttemptTime order.
func testSTClaimBacklogLostOwnerSharesQuota(t *testing.T, h Harness) {
	for _, tc := range []struct {
		name        string
		runningAt   int64
		wantRunning bool
	}{
		{"RunningOldest", stDue - 100, true},
		{"RunningNewest", stDue - 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSTFixture(t, h)
			running := f.arm(tc.runningAt)
			f.claimTask(uuid.New(), running.ID) // this owner never heartbeats
			w1 := f.arm(stDue - 50)
			w2 := f.arm(stDue - 40)
			f.arm(stDue - 30)

			req := f.claimReq(uuid.New(), true)
			req.PerTenantLimit = 2
			want := []string{w1.ID, w2.ID}
			if tc.wantRunning {
				want = []string{running.ID, w1.ID}
			}
			require.Equal(t, want, stIDs(f.claimWith(req)))
		})
	}
}
