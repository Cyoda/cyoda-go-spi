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

// A typo in a segment BEFORE the last one (the common shape for this
// suite's keys, e.g. "Entity/CompareAndSave/Conflict") must still be
// reported when the key's top-level group ran. A parent-path check (rather
// than group-only) would make the corrupted parent path match nothing and
// silently swallow this typo, which is exactly the gap this test guards.
func TestSkipTracker_UnusedKeys_IntermediateSegmentTypoReported(t *testing.T) {
	tracker := newSkipTracker()

	// The real subtest ran under "TxStateErrors".
	tracker.recordRan("Transaction/TxStateErrors/OpAfterRollback")

	skip := map[string]string{
		// Typo in the middle segment: "TxStateError" vs "TxStateErrors".
		"Transaction/TxStateError/OpAfterRollback": "stale/typo entry",
	}

	got := tracker.unusedKeys(skip)
	if len(got) != 1 || got[0] != "Transaction/TxStateError/OpAfterRollback" {
		t.Fatalf("expected the mid-segment typo key to be reported, got %v", got)
	}
}

// A 2-segment key (a leaf directly under its group, no intermediate
// segment — e.g. "AsyncSearch/Cancel") follows the same group-only rule: a
// typo is reported once the group ran.
func TestSkipTracker_UnusedKeys_TwoSegmentKeyDirectlyUnderGroup(t *testing.T) {
	tracker := newSkipTracker()

	tracker.recordRan("AsyncSearch/Cancel")

	skip := map[string]string{
		// Typo: "Cancle" vs "Cancel".
		"AsyncSearch/Cancle": "stale/typo entry",
	}

	got := tracker.unusedKeys(skip)
	if len(got) != 1 || got[0] != "AsyncSearch/Cancle" {
		t.Fatalf("expected the 2-segment typo key to be reported, got %v", got)
	}
}

// A Skip key that names a parent path rather than any actual subtest (e.g.
// "Transaction/Savepoint" when only its children —
// "Transaction/Savepoint/ReleaseMergesWork" and
// "Transaction/Savepoint/RollbackToDiscards" — ever run as subtests) is
// never matched by children running: matching is exact-string equality
// against a subtest's full path (skipIfRegistered's map lookup), done at
// the moment that exact subtest runs, not a prefix relationship. Since its
// top-level group ran, it is reported as unused — such a key can never be
// matched and is, correctly, always flagged once its group runs. It would
// stop being reported only if the tracker recorded an exact match for it
// (i.e. some real subtest were itself named exactly "Transaction/Savepoint"
// and skipIfRegistered called recordMatch for that literal name), which
// this suite's structure never does for a group-prefix key.
func TestSkipTracker_UnusedKeys_KeyNamingParentPathNeverMatches(t *testing.T) {
	tracker := newSkipTracker()

	tracker.recordRan("Transaction/Savepoint/ReleaseMergesWork")
	tracker.recordRan("Transaction/Savepoint/RollbackToDiscards")

	skip := map[string]string{
		"Transaction/Savepoint": "reason",
	}

	got := tracker.unusedKeys(skip)
	if len(got) != 1 || got[0] != "Transaction/Savepoint" {
		t.Fatalf("expected the parent-path key to be reported, got %v", got)
	}

	// Demonstrating the other half of the rule: if the tracker DID record
	// an exact match for that literal name (as it would if some subtest
	// really were named exactly "Transaction/Savepoint"), it is excluded.
	tracker.recordMatch("Transaction/Savepoint")
	got = tracker.unusedKeys(skip)
	if len(got) != 0 {
		t.Fatalf("expected no unused keys once matched, got %v", got)
	}
}
