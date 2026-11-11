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
