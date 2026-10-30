package spitest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// fakeClock is the business clock the Clock cases hand to
// Harness.ClockFactory. It is set far from real time, so a store that reads
// physical time where it must read the business clock is plain to see.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// clockStart is where the fake clock begins: years away from real time.
var clockStart = time.Date(2015, 3, 1, 0, 0, 0, 0, time.UTC)

// clockFloorStep is the step by which a store's floor may move a stamp
// above the clock's reading.
const clockFloorStep = time.Microsecond

func runClockSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "SubmitTimeFollowsClock", testClockSubmitTimeFollowsClock)
	runSubtest(t, h, tracker, "ConsistencyTimeFollowsClock", testClockConsistencyTimeFollowsClock)
	runSubtest(t, h, tracker, "FloorRefusesNothingBackward", testClockFloorRefusesNothingBackward)
	runSubtest(t, h, tracker, "SubmitTimeRetainedUnderPastClock", testClockSubmitTimeRetainedUnderPastClock)
	runSubtest(t, h, tracker, "ReapUsesClock", testClockReapUsesClock)
	runSubtest(t, h, tracker, "OperationalStaysPhysical", testClockOperationalStaysPhysical)
	runSubtest(t, h, tracker, "HeartbeatStampedFromPhysicalClock", testClockHeartbeatStampedFromPhysicalClock)
	runSubtest(t, h, tracker, "LatestRecordedTimeFreshIsZero", testClockLatestRecordedTimeFreshIsZero)
	runSubtest(t, h, tracker, "LatestRecordedTimeFollowsCommits", testClockLatestRecordedTimeFollowsCommits)
	runSubtest(t, h, tracker, "LatestRecordedTimeDoesNotRaise", testClockLatestRecordedTimeDoesNotRaise)
}

// clockStore is a store built on a fresh factory with a fake business clock.
type clockStore struct {
	h     Harness // h with Factory replaced by the clock-bearing factory
	clock *fakeClock
	tid   spi.TenantID
	ctx   context.Context
}

// newClockStore builds a factory with WithClock(fake at start) through
// Harness.ClockFactory and closes it when the subtest ends. It skips the
// subtest when the harness has no ClockFactory.
func newClockStore(t *testing.T, h Harness, start time.Time) *clockStore {
	t.Helper()
	if h.ClockFactory == nil {
		t.Skip("harness has no ClockFactory")
	}
	fake := newFakeClock(start)
	factory, closeFactory := h.ClockFactory(fake)
	require.NotNil(t, factory, "ClockFactory must return a factory")
	if closeFactory != nil {
		t.Cleanup(closeFactory)
	}
	ch := h
	ch.Factory = factory
	tid := h.NewTenant()
	return &clockStore{h: ch, clock: fake, tid: tid, ctx: tenantContext(tid)}
}

// commit saves one new entity in a transaction and commits it.
func (c *clockStore) commit(t *testing.T) (txID, entityID string) {
	t.Helper()
	return commitOne(t, c.h, c.ctx, "clock-"+uuid.NewString())
}

func (c *clockStore) submitTime(t *testing.T, txID string) time.Time {
	t.Helper()
	tm, err := c.h.Factory.TransactionManager(c.ctx)
	require.NoError(t, err)
	submit, err := tm.GetSubmitTime(c.ctx, txID)
	require.NoError(t, err)
	return submit
}

func testClockSubmitTimeFollowsClock(t *testing.T, h Harness) {
	c := newClockStore(t, h, clockStart)
	txID, entityID := c.commit(t)
	submit := c.submitTime(t, txID)
	require.WithinDuration(t, clockStart, submit, clockFloorStep,
		"the submit time is the business clock's reading, not physical time")

	es, err := c.h.Factory.EntityStore(c.ctx)
	require.NoError(t, err)
	got, err := es.Get(c.ctx, entityID)
	require.NoError(t, err)
	require.True(t, submit.Equal(got.Meta.CreationDate),
		"CreationDate %v must equal the submit time %v", got.Meta.CreationDate, submit)

	versions, err := es.GetVersionMetadata(c.ctx, entityID, spi.VersionMetadataOptions{})
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.True(t, submit.Equal(versions[0].Timestamp),
		"the audit timestamp %v must equal the submit time %v", versions[0].Timestamp, submit)
}

func testClockConsistencyTimeFollowsClock(t *testing.T, h Harness) {
	c := newClockStore(t, h, clockStart)
	c.commit(t)
	tm, err := c.h.Factory.TransactionManager(c.ctx)
	require.NoError(t, err)
	ct, err := tm.ConsistencyTime(c.ctx)
	require.NoError(t, err)
	now := c.clock.Now()
	require.False(t, ct.Before(now), "consistency time %v must not precede the clock %v", ct, now)
	require.True(t, ct.Before(now.Add(time.Second)),
		"consistency time %v must stay within a second of the clock %v", ct, now)
}

// A clock that moves backward never moves a stamp backward: the store's
// floor holds each stamp above the one before.
func testClockFloorRefusesNothingBackward(t *testing.T, h Harness) {
	c := newClockStore(t, h, clockStart)
	first, _ := c.commit(t)
	firstSubmit := c.submitTime(t, first)

	c.clock.Set(clockStart.Add(-time.Hour))
	second, _ := c.commit(t)
	secondSubmit := c.submitTime(t, second)
	require.True(t, secondSubmit.After(firstSubmit),
		"a commit after the clock went back must stamp above the first: %v then %v", firstSubmit, secondSubmit)
}

// The submit-time entries are business stamps: how long the store keeps
// them is measured on the business clock, so the physical clock running on
// does not retire a stamp the business clock has not aged.
func testClockSubmitTimeRetainedUnderPastClock(t *testing.T, h Harness) {
	c := newClockStore(t, h, clockStart)
	txID, _ := c.commit(t)
	want := c.submitTime(t, txID)

	h.AdvanceClock(2 * time.Hour)
	got := c.submitTime(t, txID)
	require.True(t, want.Equal(got), "GetSubmitTime must still answer %v after the physical clock ran on, got %v", want, got)
}

func testClockReapUsesClock(t *testing.T, h Harness) {
	c := newClockStore(t, h, clockStart)
	as, err := c.h.Factory.AsyncSearchStore(c.ctx)
	require.NoError(t, err)
	tid := c.tid
	id := newID()
	job := newSearchJob(c.h, tid, id)
	job.CreateTime = c.clock.Now()
	require.NoError(t, as.CreateJob(c.ctx, job))
	require.NoError(t, as.UpdateJobStatus(c.ctx, id, 1, "SUCCESSFUL", 0, "", c.clock.Now(), 0))

	_, err = as.ReapExpired(context.Background(), time.Hour)
	require.NoError(t, err)
	_, err = as.GetJob(c.ctx, id)
	require.NoError(t, err, "a job finished at the clock's now is not expired")

	c.clock.Advance(61 * time.Minute)
	n, err := as.ReapExpired(context.Background(), time.Hour)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)
	_, err = as.GetJob(c.ctx, id)
	require.ErrorIs(t, err, spi.ErrNotFound, "once the business clock passes the TTL the job is reaped")
}

// Liveness is physical: a business clock a year ahead of real time must not
// make a live search job look stale, nor a live scheduler owner look dead.
func testClockOperationalStaysPhysical(t *testing.T, h Harness) {
	c := newClockStore(t, h, h.Now().UTC().AddDate(1, 0, 0))

	as, err := c.h.Factory.AsyncSearchStore(c.ctx)
	require.NoError(t, err)
	tid := c.tid
	id := newID()
	job := newSearchJob(c.h, tid, id)
	job.CreateTime = c.clock.Now()
	require.NoError(t, as.CreateJob(c.ctx, job))
	require.NoError(t, as.Heartbeat(c.ctx, id, 1))
	claimed, err := as.ClaimStale(c.ctx, claimStaleAfter, 1000)
	require.NoError(t, err)
	require.Nil(t, findClaimed(claimed, id), "a job heartbeated just now is live, whatever the business clock reads")

	f := newSTFixture(t, c.h)
	owner := uuid.New()
	require.NoError(t, f.sts.Heartbeat(context.Background(), owner))
	require.NoError(t, f.sts.SweepOwners(context.Background(), claimStaleAfter))
	task := f.armDue()
	claim := f.claimTask(owner, task.ID)
	require.Empty(t, f.claimWith(f.claimReq(uuid.New(), true)),
		"an owner that just heartbeated survives SweepOwners and is not lost, whatever the business clock reads")
	requireUnchangedClaim(t, claim, f.mustGet(task.ID))
}

// CreateJob stamps HeartbeatTime from the store's physical clock even when a
// business clock is supplied: the stamp sits near real time, not near the
// fake clock's 2015.
func testClockHeartbeatStampedFromPhysicalClock(t *testing.T, h Harness) {
	c := newClockStore(t, h, clockStart)
	as, err := c.h.Factory.AsyncSearchStore(c.ctx)
	require.NoError(t, err)
	id := newID()
	job := newSearchJob(c.h, c.tid, id)
	job.CreateTime = c.clock.Now()
	require.NoError(t, as.CreateJob(c.ctx, job))
	got, err := as.GetJob(c.ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got.HeartbeatTime, "CreateJob must stamp HeartbeatTime")
	require.WithinDuration(t, h.Now(), *got.HeartbeatTime, 5*time.Second,
		"the heartbeat stamp is physical time, not the business clock's %v", c.clock.Now())
}

func (c *clockStore) latestRecorded(t *testing.T) time.Time {
	t.Helper()
	tm, err := c.h.Factory.TransactionManager(c.ctx)
	require.NoError(t, err)
	latest, err := tm.LatestRecordedTime(c.ctx)
	require.NoError(t, err)
	return latest
}

// A store that has issued no time records none.
func testClockLatestRecordedTimeFreshIsZero(t *testing.T, h Harness) {
	c := newClockStore(t, h, clockStart)
	got := c.latestRecorded(t)
	require.True(t, got.IsZero(), "a fresh store has recorded no time, got %v", got)
}

func testClockLatestRecordedTimeFollowsCommits(t *testing.T, h Harness) {
	c := newClockStore(t, h, clockStart)
	txID, _ := c.commit(t)
	submit := c.submitTime(t, txID)
	got := c.latestRecorded(t)
	require.True(t, submit.Equal(got), "after a commit the latest recorded time is its submit time %v, got %v", submit, got)
}

// Reading the latest recorded time leaves the floor where it is: a clock set
// back below the read's moment still stamps there.
func testClockLatestRecordedTimeDoesNotRaise(t *testing.T, h Harness) {
	c := newClockStore(t, h, clockStart)
	c.commit(t)
	at := clockStart.Add(2 * time.Hour)
	c.clock.Set(at)
	first := c.latestRecorded(t)
	second := c.latestRecorded(t)
	require.True(t, first.Equal(second), "two reads with nothing in between differ: %v then %v", first, second)
	require.True(t, first.Before(at), "the read %v must not reach the clock %v", first, at)

	back := at.Add(-time.Hour)
	c.clock.Set(back)
	txID, _ := c.commit(t)
	submit := c.submitTime(t, txID)
	require.True(t, submit.After(first), "the commit %v must stamp above the earlier read %v", submit, first)
	require.False(t, submit.After(back.Add(time.Second)),
		"the commit %v must stamp at the clock %v: the read lifted the floor", submit, back)
}
