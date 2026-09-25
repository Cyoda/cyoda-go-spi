package spitest

import "testing"

// These tests exercise skipTracker's "unused key" logic directly, without
// running `go test -run` against the full conformance suite: they simulate
// what a filtered run leaves behind by calling recordRan/recordMatch the
// same way skipIfRegistered does, then assert what unusedKeys reports.

// A Skip key whose group never ran (filtered out by `-run`) must not be
// reported: the harness never had a chance to match it, so reporting it
// would fail focused runs on backends with skip entries in an unrelated
// group.
func TestSkipTracker_UnusedKeys_FilteredOutGroupNotReported(t *testing.T) {
	tracker := newSkipTracker()

	// Simulates `-run 'TestConformance/AsyncSearch'`: only AsyncSearch
	// subtests ran. Transaction never did.
	tracker.recordRan("AsyncSearch/CreateAndGet")

	skip := map[string]string{
		"Transaction/TxStateErrors/OpAfterRollback": "backend does not support mid-tx rollback errors",
	}

	got := tracker.unusedKeys(skip)
	if len(got) != 0 {
		t.Fatalf("expected no unused keys for a filtered-out group, got %v", got)
	}
}

// A typo in a Skip key whose group DID run must still be reported: the
// group had every chance to match, and nothing did.
func TestSkipTracker_UnusedKeys_TypoInRanGroupReported(t *testing.T) {
	tracker := newSkipTracker()

	// The real subtest ran (and, since its name doesn't equal the typo'd
	// key below, was never skipped).
	tracker.recordRan("Transaction/TxStateErrors/OpAfterRollback")

	skip := map[string]string{
		// Typo: "OpAfterRollback" misspelled.
		"Transaction/TxStateErrors/OpAfterRollbackk": "stale/typo entry",
	}

	got := tracker.unusedKeys(skip)
	if len(got) != 1 || got[0] != "Transaction/TxStateErrors/OpAfterRollbackk" {
		t.Fatalf("expected the typo key to be reported, got %v", got)
	}
}

// A key that was actually matched (the subtest ran and was skipped by it)
// is never reported, regardless of what else ran.
func TestSkipTracker_UnusedKeys_MatchedKeyNotReported(t *testing.T) {
	tracker := newSkipTracker()

	tracker.recordRan("Transaction/TxStateErrors/OpAfterRollback")
	tracker.recordMatch("Transaction/TxStateErrors/OpAfterRollback")

	skip := map[string]string{
		"Transaction/TxStateErrors/OpAfterRollback": "backend does not support mid-tx rollback errors",
	}

	got := tracker.unusedKeys(skip)
	if len(got) != 0 {
		t.Fatalf("expected no unused keys, matched key was reported: %v", got)
	}
}
