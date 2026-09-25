package spitest

import "testing"

// These tests exercise skipTracker and shouldCheckSkipKeys directly,
// without running `go test -run` against the full conformance suite.

// shouldCheckSkipKeys must be true only for an unfiltered run (the
// `test.run` flag empty). `-run` can filter this suite at any segment
// depth, and no fixed grain of "did this key's subtest have a chance to
// run" comparison is correct at every depth, so the check simply does not
// run under any filter — any non-empty pattern, at any depth, disables it.
func TestShouldCheckSkipKeys(t *testing.T) {
	cases := []struct {
		name    string
		runFlag string
		want    bool
	}{
		{"empty run flag: unfiltered", "", true},
		{"top-level group filter", "TestConformance/AsyncSearch", false},
		{"deep filter below group level", "TestConformance/Transaction/TxStateErrors/JoinAfterCommit", false},
		{"bare test-name filter", "TestConformance", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shouldCheckSkipKeys(c.runFlag)
			if got != c.want {
				t.Fatalf("shouldCheckSkipKeys(%q) = %v, want %v", c.runFlag, got, c.want)
			}
		})
	}
}

// On an unfiltered run — the only case callers use unusedKeys for, gated
// by shouldCheckSkipKeys — it must report every unmatched Skip key
// regardless of shape: a leaf typo, a typo in an intermediate segment, and
// a plain stale entry. It must never report a key that was actually
// matched.
func TestSkipTracker_UnusedKeys_UnfilteredRun(t *testing.T) {
	tracker := newSkipTracker()

	// The subtests that actually ran and matched their own Skip keys.
	tracker.recordMatch("Transaction/TxStateErrors/OpAfterRollback")
	tracker.recordMatch("AsyncSearch/Cancel")

	skip := map[string]string{
		"Transaction/TxStateErrors/OpAfterRollback": "matched — must not be reported",
		// Leaf typo: extra trailing "k".
		"Transaction/TxStateErrors/OpAfterRollbackk": "leaf typo",
		// Intermediate-segment typo: "TxStateError" is missing the "s".
		"Transaction/TxStateError/OpAfterRollback": "intermediate-segment typo",
		// Stale entry: no such subtest was ever registered.
		"Entity/NoSuchSubtest/StaleEntry": "stale entry",
	}

	got := tracker.unusedKeys(skip)

	want := map[string]bool{
		"Transaction/TxStateErrors/OpAfterRollbackk": true,
		"Transaction/TxStateError/OpAfterRollback":   true,
		"Entity/NoSuchSubtest/StaleEntry":            true,
	}
	if len(got) != len(want) {
		t.Fatalf("unusedKeys = %v, want exactly the keys in %v", got, want)
	}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unusedKeys reported %q, which was not expected (matched or not a Skip key under test)", k)
		}
	}
}
