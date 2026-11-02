package spitest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func runPingSuite(t *testing.T, h Harness, tracker *skipTracker) {
	runSubtest(t, h, tracker, "Healthy", testPingHealthy)
	runSubtest(t, h, tracker, "CancelledContext", testPingCancelledContext)
	runSubtest(t, h, tracker, "AfterClose", testPingAfterClose)
}

// A live store answers.
func testPingHealthy(t *testing.T, h Harness) {
	require.NoError(t, h.Factory.Ping(context.Background()))
}

// Ping returns an error when its context is already done.
func testPingCancelledContext(t *testing.T, h Harness) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, h.Factory.Ping(ctx))
}

// After Close, Ping returns an error. It runs on a fresh factory from
// Harness.ClockFactory because the shared factory is closed at the end of
// the run.
func testPingAfterClose(t *testing.T, h Harness) {
	if h.ClockFactory == nil {
		t.Skip("harness has no ClockFactory")
	}
	f, closeFn := h.ClockFactory(newFakeClock(clockStart))
	require.NotNil(t, f, "ClockFactory must return a factory")
	ctx := context.Background()
	require.NoError(t, f.Ping(ctx))
	require.NotNil(t, closeFn, "ClockFactory must return a close function")
	closeFn()
	require.Error(t, f.Ping(ctx))
}
