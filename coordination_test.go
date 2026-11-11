package spi

import (
	"errors"
	"fmt"
	"testing"
)

// TestCoordinationSentinels pins the three coordination sentinels: each is
// distinct from the others and from the sentinels a caller classifies
// alongside them, each survives wrapping, and the messages are the documented
// ones.
func TestCoordinationSentinels(t *testing.T) {
	all := map[string]error{
		"ErrCalloutSuperseded": ErrCalloutSuperseded,
		"ErrTxRollbackOnly":    ErrTxRollbackOnly,
		"ErrScopeConflict":     ErrScopeConflict,
		"ErrConflict":          ErrConflict,
		"ErrNotFound":          ErrNotFound,
		"ErrTxTerminated":      ErrTxTerminated,
	}
	for an, a := range all {
		for bn, b := range all {
			if an != bn && errors.Is(a, b) {
				t.Errorf("%s must not match %s", an, bn)
			}
		}
	}
	want := map[error]string{
		ErrCalloutSuperseded: "callout superseded",
		ErrTxRollbackOnly:    "transaction is rollback-only",
		ErrScopeConflict:     "scope conflicts with another scope of the transaction",
	}
	for err, msg := range want {
		if !errors.Is(fmt.Errorf("coordinator: %w", err), err) {
			t.Errorf("wrapped %q must match via errors.Is", msg)
		}
		if err.Error() != msg {
			t.Errorf("message = %q, want %q", err.Error(), msg)
		}
	}
}

// TestRollbackOnlyError pins the refusal for a mark: it matches
// ErrTxRollbackOnly under errors.Is, wrapped or not, and nothing else; it is
// found by errors.As with its code; and its message says rollback-only and
// names the code.
func TestRollbackOnlyError(t *testing.T) {
	var err error = &RollbackOnlyError{FailedCode: "VALIDATION_FAILED"}
	wrapped := fmt.Errorf("coordinator: %w", err)
	for name, e := range map[string]error{"bare": err, "wrapped": wrapped} {
		if !errors.Is(e, ErrTxRollbackOnly) {
			t.Errorf("%s: must match ErrTxRollbackOnly", name)
		}
		for _, other := range []error{ErrCalloutSuperseded, ErrScopeConflict, ErrConflict, ErrNotFound, ErrTxTerminated} {
			if errors.Is(e, other) {
				t.Errorf("%s: must not match %v", name, other)
			}
		}
		var roe *RollbackOnlyError
		if !errors.As(e, &roe) {
			t.Fatalf("%s: errors.As must find the *RollbackOnlyError", name)
		}
		if roe.FailedCode != "VALIDATION_FAILED" {
			t.Errorf("%s: FailedCode = %q, want VALIDATION_FAILED", name, roe.FailedCode)
		}
	}
	if errors.Is(ErrTxRollbackOnly, err) {
		t.Error("the sentinel must not match a RollbackOnlyError")
	}
	if want := "transaction is rollback-only (failed with VALIDATION_FAILED)"; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}
