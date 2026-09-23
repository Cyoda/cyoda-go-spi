package spitest

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func runAuditSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "RecordAndGet", testAuditRecordAndGet)
	runSubtest(t, h, tracker, "EventID", testAuditEventID)
	runSubtest(t, h, tracker, "GetEvents/Ordering", testAuditGetEventsOrdering)
	runSubtest(t, h, tracker, "GetEvents/NotFound", testAuditGetEventsNotFound)
	runSubtest(t, h, tracker, "GetEventsByTransaction", testAuditGetByTx)
	runSubtest(t, h, tracker, "TenantIsolation", testAuditTenantIsolation)
}

func newSMEvent(txID, state, details string) spi.StateMachineEvent {
	return spi.StateMachineEvent{
		EventType:     spi.SMEventTransitionMade,
		TransactionID: txID,
		State:         state,
		Details:       details,
		Timestamp:     time.Now().UTC(),
	}
}

func testAuditRecordAndGet(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	as, err := h.Factory.StateMachineAuditStore(ctx)
	require.NoError(t, err)
	require.NoError(t, as.Record(ctx, "e1", newSMEvent("tx1", "B", "A->B")))
	events, err := as.GetEvents(ctx, "e1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "B", events[0].State)
	require.Equal(t, "tx1", events[0].TransactionID)
}

// testAuditEventID pins that the event id is the store's: every recorded
// event comes back with a non-empty, distinct, version-1 UUID, a caller's
// value is ignored, and the ids are the same on every read and through
// both reads.
func testAuditEventID(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	as, err := h.Factory.StateMachineAuditStore(ctx)
	require.NoError(t, err)

	const callerID = "caller-chosen-id"
	withCaller := newSMEvent("tx1", "C", "B->C")
	withCaller.TimeUUID = callerID
	require.NoError(t, as.Record(ctx, "e1", newSMEvent("tx1", "B", "A->B")))
	require.NoError(t, as.Record(ctx, "e1", withCaller))
	require.NoError(t, as.Record(ctx, "e1", newSMEvent("tx2", "D", "C->D")))

	first, err := as.GetEvents(ctx, "e1")
	require.NoError(t, err)
	require.Len(t, first, 3)
	seen := map[string]bool{}
	byState := map[string]string{}
	for _, ev := range first {
		require.NotEmpty(t, ev.TimeUUID, "event %q has no id", ev.State)
		id, perr := uuid.Parse(ev.TimeUUID)
		require.NoError(t, perr, "event %q id %q is not a UUID", ev.State, ev.TimeUUID)
		require.Equal(t, uuid.Version(1), id.Version(), "event %q id %q is not version 1", ev.State, ev.TimeUUID)
		require.NotEqual(t, callerID, ev.TimeUUID, "the caller's id must be ignored")
		require.False(t, seen[ev.TimeUUID], "id %q returned twice", ev.TimeUUID)
		seen[ev.TimeUUID] = true
		byState[ev.State] = ev.TimeUUID
	}

	second, err := as.GetEvents(ctx, "e1")
	require.NoError(t, err)
	require.Len(t, second, 3)
	for _, ev := range second {
		require.Equal(t, byState[ev.State], ev.TimeUUID, "event %q id changed between reads", ev.State)
	}

	byTx, err := as.GetEventsByTransaction(ctx, "e1", "tx1")
	require.NoError(t, err)
	require.Len(t, byTx, 2)
	for _, ev := range byTx {
		require.Equal(t, byState[ev.State], ev.TimeUUID, "event %q id differs between GetEvents and GetEventsByTransaction", ev.State)
	}
}

func testAuditGetEventsOrdering(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	as, _ := h.Factory.StateMachineAuditStore(ctx)
	require.NoError(t, as.Record(ctx, "e1", newSMEvent("tx1", "B", "A->B")))
	require.NoError(t, as.Record(ctx, "e1", newSMEvent("tx2", "C", "B->C")))
	require.NoError(t, as.Record(ctx, "e1", newSMEvent("tx3", "D", "C->D")))
	events, err := as.GetEvents(ctx, "e1")
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Equal(t, "B", events[0].State)
	require.Equal(t, "C", events[1].State)
	require.Equal(t, "D", events[2].State)
}

func testAuditGetEventsNotFound(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	as, _ := h.Factory.StateMachineAuditStore(ctx)
	events, err := as.GetEvents(ctx, "never")
	require.NoError(t, err)
	require.Len(t, events, 0, "unknown entity returns empty slice, not ErrNotFound")
}

func testAuditGetByTx(t *testing.T, h Harness) {
	ctx := tenantContext(h.NewTenant())
	as, _ := h.Factory.StateMachineAuditStore(ctx)
	require.NoError(t, as.Record(ctx, "e1", newSMEvent("tx1", "B", "A->B")))
	require.NoError(t, as.Record(ctx, "e1", newSMEvent("tx2", "C", "B->C")))
	require.NoError(t, as.Record(ctx, "e1", newSMEvent("tx1", "D", "C->D"))) // tx1 reused
	events, err := as.GetEventsByTransaction(ctx, "e1", "tx1")
	require.NoError(t, err)
	require.Len(t, events, 2)
}

func testAuditTenantIsolation(t *testing.T, h Harness) {
	tA, tB := h.NewTenant(), h.NewTenant()
	asA, _ := h.Factory.StateMachineAuditStore(tenantContext(tA))
	asB, _ := h.Factory.StateMachineAuditStore(tenantContext(tB))
	require.NoError(t, asA.Record(tenantContext(tA), "e1", newSMEvent("tx", "B", "A->B")))
	events, err := asB.GetEvents(tenantContext(tB), "e1")
	require.NoError(t, err)
	require.Len(t, events, 0)
}
