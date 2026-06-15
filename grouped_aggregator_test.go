package spi_test

import (
	"testing"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

func TestGroupedAggregatorContract(t *testing.T) {
	// Runtime semantics are exercised by plugin parity tests in cyoda-go's
	// e2e/parity registry; the GroupedAggregator interface compile check is
	// already enforced by the package compiling. This test catches struct-
	// literal renames and empty enum constants at SPI-consumer compile time.

	// GroupExpr literal-init check (both Kind values).
	_ = spi.GroupExpr{Kind: spi.GroupExprState}
	_ = spi.GroupExpr{Kind: spi.GroupExprDataPath, Path: "$.x"}

	// AggregateOp enumeration check — guards against an empty constant.
	for _, op := range []spi.AggregateOp{
		spi.AggSum, spi.AggAvg, spi.AggMin, spi.AggMax, spi.AggStdev,
	} {
		if op == "" {
			t.Fatalf("aggregate op is empty")
		}
	}
}
