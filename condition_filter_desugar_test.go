package spi_test

import (
	"testing"

	spi "github.com/cyoda-platform/cyoda-go-spi"
	"github.com/cyoda-platform/cyoda-go-spi/predicate"
)

// deepChainCondition builds a chain of depth nested AND-groups, each wrapping
// exactly one child: a leaf SimpleCondition at the bottom, a nested
// GroupCondition everywhere above it. This is the shape that exposes
// re-desugaring: [ConditionToFilter] desugars the WHOLE tree once at the top
// (spi.DesugarCondition recurses into every GroupCondition descendant in
// that one call), then dispatches a GroupCondition to groupToFilter, which —
// before the fix this test drives — called [ConditionToFilter] again for
// each child, re-desugaring that child's ALREADY-desugared subtree. For a
// depth-D chain that telescopes into D + (D-1) + ... + 1 = O(D²) group-node
// revisits instead of O(D).
func deepChainCondition(depth int) predicate.Condition {
	var c predicate.Condition = &predicate.SimpleCondition{
		JsonPath: "$.leaf", OperatorType: "EQUALS", Value: "x",
	}
	for i := 0; i < depth; i++ {
		c = &predicate.GroupCondition{Operator: "AND", Conditions: []predicate.Condition{c}}
	}
	return c
}

// TestConditionToFilter_DesugarIsNotReappliedPerLevel pins that
// [spi.DesugarCondition]'s per-group allocation (`children :=
// make([]predicate.Condition, len(v.Conditions))`) happens ONCE per group
// node across a whole [spi.ConditionToFilter] call, not once per group node
// PER ANCESTOR LEVEL. testing.AllocsPerRun is used rather than a wall-clock
// bound because it is deterministic — no flakiness from CI/sandbox
// scheduling noise — and every group-node revisit this bug causes allocates
// at least one slice, so it is a faithful proxy for the redundant-recursion
// count.
//
// The assertion compares the allocation count at two chain depths rather
// than pinning an absolute number: doubling the depth of a LINEAR
// (non-redundant) desugar roughly doubles the allocation count, while the
// pre-fix QUADRATIC behaviour roughly QUADRUPLES it. Requiring the observed
// ratio to stay well under quadratic (using 3x as the cutoff, comfortably
// between the 2x a correct implementation produces and the ~4x a quadratic
// one would) catches the regression without hard-coding an allocator-version
// -sensitive absolute count.
func TestConditionToFilter_DesugarIsNotReappliedPerLevel(t *testing.T) {
	const small, large = 40, 80

	condSmall := deepChainCondition(small)
	condLarge := deepChainCondition(large)

	allocsSmall := testing.AllocsPerRun(20, func() {
		if _, err := spi.ConditionToFilter(condSmall, nil); err != nil {
			t.Fatalf("ConditionToFilter(depth=%d): %v", small, err)
		}
	})
	allocsLarge := testing.AllocsPerRun(20, func() {
		if _, err := spi.ConditionToFilter(condLarge, nil); err != nil {
			t.Fatalf("ConditionToFilter(depth=%d): %v", large, err)
		}
	})

	if allocsSmall <= 0 {
		t.Fatalf("sanity: allocsSmall = %v, want > 0", allocsSmall)
	}
	ratio := allocsLarge / allocsSmall
	if ratio > 3 {
		t.Errorf("doubling chain depth (%d -> %d) multiplied allocations by %.2fx (allocsSmall=%v allocsLarge=%v); "+
			"want well under 4x (quadratic) — DesugarCondition is being re-applied per ancestor level instead of once for the whole tree",
			small, large, ratio, allocsSmall, allocsLarge)
	}
}
