package spi

import (
	"errors"
	"fmt"
	"testing"
)

func TestSentinelsAreDistinct(t *testing.T) {
	sentinels := []error{ErrNotFound, ErrConflict, ErrEpochMismatch}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Errorf("sentinel %d (%v) should not match %d (%v)", i, a, j, b)
			}
		}
	}
}

func TestSentinelsAreMatchedAfterWrap(t *testing.T) {
	wrapped := fmt.Errorf("store layer: %w", ErrNotFound)
	if !errors.Is(wrapped, ErrNotFound) {
		t.Fatal("wrapped ErrNotFound should match via errors.Is")
	}
}

func TestErrRetryExhausted_DistinctFromErrConflict(t *testing.T) {
	if errors.Is(ErrRetryExhausted, ErrConflict) {
		t.Error("ErrRetryExhausted must not unwrap to ErrConflict — they are distinct failure modes")
	}
	if errors.Is(ErrConflict, ErrRetryExhausted) {
		t.Error("ErrConflict must not unwrap to ErrRetryExhausted")
	}
	if ErrRetryExhausted.Error() == "" {
		t.Error("ErrRetryExhausted must have a non-empty message")
	}
}

// TestTxSentinelHierarchy verifies the parent/child relationships defined
// by the sentinelErr.Unwrap() chain. Every child must match its parent
// via errors.Is.
func TestTxSentinelHierarchy(t *testing.T) {
	positive := []struct {
		name   string
		child  error
		parent error
	}{
		{"ErrTxNotFound→ErrNotFound", ErrTxNotFound, ErrNotFound},
		{"ErrSavepointNotFound→ErrNotFound", ErrSavepointNotFound, ErrNotFound},
		{"ErrTxRolledBack→ErrTxTerminated", ErrTxRolledBack, ErrTxTerminated},
		{"ErrTxAlreadyCommitted→ErrTxTerminated", ErrTxAlreadyCommitted, ErrTxTerminated},
	}
	for _, tc := range positive {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.child, tc.parent) {
				t.Errorf("expected errors.Is(%v, %v) == true", tc.child, tc.parent)
			}
		})
	}
}

// TestTxSentinelsAreDistinct verifies that siblings under a shared parent
// and unrelated sentinels do not match each other via errors.Is. These
// negative pairs are load-bearing for callers that distinguish conditions.
func TestTxSentinelsAreDistinct(t *testing.T) {
	negative := []struct {
		name string
		a    error
		b    error
	}{
		// Siblings under ErrNotFound — tx-not-found vs savepoint-not-found
		// must stay distinguishable.
		{"ErrTxNotFound!~ErrSavepointNotFound", ErrTxNotFound, ErrSavepointNotFound},
		{"ErrSavepointNotFound!~ErrTxNotFound", ErrSavepointNotFound, ErrTxNotFound},

		// Siblings under ErrTxTerminated — rolled-back vs already-committed
		// must stay distinguishable for diagnostic purposes.
		{"ErrTxRolledBack!~ErrTxAlreadyCommitted", ErrTxRolledBack, ErrTxAlreadyCommitted},
		{"ErrTxAlreadyCommitted!~ErrTxRolledBack", ErrTxAlreadyCommitted, ErrTxRolledBack},

		// CommitInProgress is a transient race, NOT a terminal state.
		// It must not match the ErrTxTerminated umbrella.
		{"ErrTxCommitInProgress!~ErrTxTerminated", ErrTxCommitInProgress, ErrTxTerminated},
		{"ErrTxTerminated!~ErrTxCommitInProgress", ErrTxTerminated, ErrTxCommitInProgress},

		// Cross-tree pairs.
		{"ErrTxRolledBack!~ErrNotFound", ErrTxRolledBack, ErrNotFound},
		{"ErrTxAlreadyCommitted!~ErrNotFound", ErrTxAlreadyCommitted, ErrNotFound},
		{"ErrTxNotFound!~ErrTxTerminated", ErrTxNotFound, ErrTxTerminated},
		{"ErrSavepointNotFound!~ErrTxTerminated", ErrSavepointNotFound, ErrTxTerminated},
		{"ErrTxTenantMismatch!~ErrTxTerminated", ErrTxTenantMismatch, ErrTxTerminated},
		{"ErrTxTenantMismatch!~ErrNotFound", ErrTxTenantMismatch, ErrNotFound},
		{"ErrTxCommitInProgress!~ErrTxNotFound", ErrTxCommitInProgress, ErrTxNotFound},
		{"ErrTxRolledBack!~ErrTxCommitInProgress", ErrTxRolledBack, ErrTxCommitInProgress},
		{"ErrTxAlreadyCommitted!~ErrTxCommitInProgress", ErrTxAlreadyCommitted, ErrTxCommitInProgress},

		// Existing sentinels stay clean.
		{"ErrConflict!~ErrTxTerminated", ErrConflict, ErrTxTerminated},
		{"ErrConflict!~ErrTxRolledBack", ErrConflict, ErrTxRolledBack},
		{"ErrConflict!~ErrTxCommitInProgress", ErrConflict, ErrTxCommitInProgress},
		{"ErrTxCommitInProgress!~ErrConflict", ErrTxCommitInProgress, ErrConflict},
	}
	for _, tc := range negative {
		t.Run(tc.name, func(t *testing.T) {
			if errors.Is(tc.a, tc.b) {
				t.Errorf("expected errors.Is(%v, %v) == false", tc.a, tc.b)
			}
		})
	}
}

// TestTxSentinelWrapChain verifies that fmt.Errorf("...: %w", sentinel)
// composes correctly with the sentinelErr.Unwrap() chain — errors.Is
// must walk both layers and match against the sentinel AND its parent.
func TestTxSentinelWrapChain(t *testing.T) {
	wrapped := fmt.Errorf("plugin context: %w", ErrTxNotFound)
	if !errors.Is(wrapped, ErrTxNotFound) {
		t.Error("wrapped ErrTxNotFound should match ErrTxNotFound")
	}
	if !errors.Is(wrapped, ErrNotFound) {
		t.Error("wrapped ErrTxNotFound should match ErrNotFound via Unwrap chain")
	}
	if errors.Is(wrapped, ErrTxRolledBack) {
		t.Error("wrapped ErrTxNotFound must not match unrelated ErrTxRolledBack")
	}

	deeplyWrapped := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", ErrTxRolledBack))
	if !errors.Is(deeplyWrapped, ErrTxRolledBack) {
		t.Error("doubly-wrapped ErrTxRolledBack should match ErrTxRolledBack")
	}
	if !errors.Is(deeplyWrapped, ErrTxTerminated) {
		t.Error("doubly-wrapped ErrTxRolledBack should match ErrTxTerminated via Unwrap chain")
	}
}

// TestTxSentinelsHaveNonEmptyMessages mirrors the assertion pattern from
// TestErrRetryExhausted_DistinctFromErrConflict for the seven new
// transaction-state sentinels — every sentinel must Error() to a
// non-empty string so log lines and test failures stay informative.
func TestTxSentinelsHaveNonEmptyMessages(t *testing.T) {
	cases := map[string]error{
		"ErrTxNotFound":         ErrTxNotFound,
		"ErrSavepointNotFound":  ErrSavepointNotFound,
		"ErrTxTerminated":       ErrTxTerminated,
		"ErrTxRolledBack":       ErrTxRolledBack,
		"ErrTxAlreadyCommitted": ErrTxAlreadyCommitted,
		"ErrTxCommitInProgress": ErrTxCommitInProgress,
		"ErrTxTenantMismatch":   ErrTxTenantMismatch,
	}
	for name, sentinel := range cases {
		if sentinel.Error() == "" {
			t.Errorf("%s must have a non-empty message", name)
		}
	}
}

// TestScheduledTaskSentinels pins the three scheduled-task sentinels: each
// is distinct from the others and from the sentinels a caller classifies
// alongside them, each survives wrapping, and the messages are the
// documented ones.
func TestScheduledTaskSentinels(t *testing.T) {
	all := map[string]error{
		"ErrStaleClaim":           ErrStaleClaim,
		"ErrMarkedByAnotherClaim": ErrMarkedByAnotherClaim,
		"ErrTaskBusy":             ErrTaskBusy,
		"ErrStoreRejected":        ErrStoreRejected,
		"ErrConflict":             ErrConflict,
		"ErrNotFound":             ErrNotFound,
	}
	for an, a := range all {
		for bn, b := range all {
			if an != bn && errors.Is(a, b) {
				t.Errorf("%s must not match %s", an, bn)
			}
		}
	}
	for name, err := range map[string]error{
		"ErrMarkedByAnotherClaim": ErrMarkedByAnotherClaim,
		"ErrTaskBusy":             ErrTaskBusy,
		"ErrStoreRejected":        ErrStoreRejected,
	} {
		if !errors.Is(fmt.Errorf("store layer: %w", err), err) {
			t.Errorf("wrapped %s must match via errors.Is", name)
		}
	}
	want := map[error]string{
		ErrMarkedByAnotherClaim: "scheduled task: marked by another claim of this life",
		ErrTaskBusy:             "scheduled task: row is being written by an open transaction",
		ErrStoreRejected:        "store rejected the write deterministically",
	}
	for err, msg := range want {
		if err.Error() != msg {
			t.Errorf("message = %q, want %q", err.Error(), msg)
		}
	}
}
