package spitest

import (
	"context"
	"encoding/json"
	"iter"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	spi "github.com/cyoda-platform/cyoda-go-spi"
)

// spiCtx aliases context.Context for readability in helper signatures.
type spiCtx = context.Context

// newEntity builds a minimal Entity with the given model name, id, and
// JSON-serializable payload. Used across all subtest files.
//
// Note: spi.Entity has fields {Meta EntityMeta, Data []byte} — the ID and
// ModelRef live INSIDE Meta. Callers read them back via got.Meta.ID etc.
// ModelRef.EntityName is the model name; ModelRef.ModelVersion is the version string.
func newEntity(t *testing.T, modelName, id string, payload map[string]any) *spi.Entity {
	t.Helper()
	buf, err := json.Marshal(payload)
	require.NoError(t, err)
	return &spi.Entity{
		Meta: spi.EntityMeta{
			ID:       id,
			ModelRef: spi.ModelRef{EntityName: modelName, ModelVersion: "1"},
		},
		Data: buf,
	}
}

// withTx begins a tx, runs fn with the tx-scoped context, then commits.
// On any error (from fn or Commit), rolls back and fails the test.
//
// Use withTx ONLY for tests that need a committed baseline before the
// real assertion (e.g., "save N entities, then GetAll returns N").
// Tests that must inspect IN-FLIGHT transaction state (CommitVisibility,
// RollbackDiscards, Join, Savepoint variants) cannot use withTx — they
// need explicit control over the Begin/Commit lifecycle.
//
// Note: Commit and Rollback are called with txCtx (not the caller's ctx)
// so that backends which embed transaction state in the context (e.g.
// Cassandra) can locate the transaction object.
func withTx(t *testing.T, h Harness, ctx spiCtx, fn func(txCtx spiCtx)) {
	t.Helper()
	tm, err := h.Factory.TransactionManager(ctx)
	require.NoError(t, err)
	txID, txCtx, err := tm.Begin(ctx)
	require.NoError(t, err)
	done := false
	defer func() {
		if !done {
			_ = tm.Rollback(txCtx, txID)
		}
	}()
	fn(txCtx)
	require.NoError(t, tm.Commit(txCtx, txID))
	done = true
}

// newID generates a version-1 (time-based) UUID string for use as an entity
// or job ID. Version-1 is required by backends that store IDs in timeuuid
// columns (e.g. Cassandra); backends that accept opaque strings (e.g.
// memory, postgres) work equally well with v1 UUIDs. Conformance subtests
// must use newID() rather than short literals like "e1" or "job-1".
func newID() string {
	id, err := uuid.NewUUID() // v1 UUID
	if err != nil {
		// uuid.NewUUID only fails when the node ID cannot be set, which is
		// essentially impossible in practice. Panic so test failures surface
		// immediately rather than silently using a zero value.
		panic("spitest.newID: uuid.NewUUID failed: " + err.Error())
	}
	return id.String()
}

// newAttributedEntity builds an Entity like newEntity but also stamps the
// attribution Meta fields a caller (e.g. cyoda-go's entity service) sets
// before invoking Save — ChangeUser/ChangeUserKind/ChangeExecutor. Plugins
// must persist these verbatim and surface them via GetVersionHistory as
// EntityVersion.AttributedKind/Executor; see testEntityExecutorRoundTrip.
func newAttributedEntity(t *testing.T, modelName, id string, payload map[string]any, changeUser string, changeUserKind spi.PrincipalKind, executor spi.Principal) *spi.Entity {
	t.Helper()
	e := newEntity(t, modelName, id, payload)
	e.Meta.ChangeUser = changeUser
	e.Meta.ChangeUserKind = changeUserKind
	e.Meta.ChangeExecutor = executor
	return e
}

// iterSeq wraps a slice into an iter.Seq, matching the SaveAll interface.
func iterSeq[T any](items []T) iter.Seq[T] {
	return func(yield func(T) bool) {
		for _, it := range items {
			if !yield(it) {
				return
			}
		}
	}
}
