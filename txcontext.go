package spi

import (
	"context"
	"sync"
	"time"
)

// TransactionState holds the state of an active SSI transaction.
// All processor execution is sequential (no goroutines) — see
// docs/superpowers/specs/2026-04-01-workflow-processor-execution-design.md.
// SAVEPOINTs snapshot/restore these maps for ASYNC_NEW_TX rollback isolation.
//
// # Concurrency contract
//
// Plugin implementations of [TransactionManager] must coordinate concurrent
// access to TransactionState's mutable fields using OpMu. Two distinct
// concerns:
//
//  1. Cross-class serialisation — plugin's responsibility, enforced via
//     OpMu. In-flight tx-path operations (Save, Get, Delete, Savepoint,
//     etc., regardless of which plugin type — TransactionManager,
//     EntityStore, or any other surface — defines them) hold OpMu.RLock;
//     closure operations (Commit, Rollback, RollbackToSavepoint) hold
//     OpMu.Lock. This guarantees Commit/Rollback wait for any in-flight
//     SPI-method invocation on the same tx to drain before mutating or
//     closing tx state. Every plugin method that reads or writes
//     ReadSet, WriteSet, Buffer, Deletes, RolledBack, or Closed must
//     acquire OpMu in the appropriate posture.
//
//  2. Within-class serialisation — application's responsibility, NOT
//     enforced by OpMu. OpMu.RLock allows multiple readers concurrently;
//     it does not mutually exclude RLock-holders from each other. If the
//     application fires two RLock-holding ops on the same tx concurrently
//     (e.g. two `Save` calls from different goroutines), the underlying
//     Go map writes to tx.Buffer / tx.WriteSet / tx.Deletes will trigger
//     the runtime's "concurrent map writes" fatal — RLock does not
//     protect map writes from each other regardless of key overlap. The
//     application must serialise its own ops on a given tx; the plugin
//     does not detect or recover from this contract violation.
//
// # Lock posture per field
//
//   - ReadSet, WriteSet, Buffer, Deletes: read or written under OpMu.RLock
//     by in-flight ops; iterated or replaced under OpMu.Lock by Commit /
//     Rollback / RollbackToSavepoint.
//   - Closed: written under OpMu.Lock by Commit/Rollback in their defer
//     (so all return paths are covered); read under OpMu.RLock by every
//     in-flight op so the op fails fast on a closed tx.
//   - RolledBack: written under OpMu.Lock by Rollback eagerly inside the
//     OpMu region (not in defer); read under OpMu.RLock by every
//     in-flight op.
//   - ID, TenantID, SnapshotTime: immutable after [TransactionManager.Begin]
//     returns; safe to read without locks.
//
// # Lock order
//
// Plugin implementations acquire locks in this overall order to avoid
// deadlock:
//
//	tx.OpMu  →  factory's per-store mutex  →  manager's per-tx-table mutex
//
// The manager's per-tx-table mutex is also acquired BEFORE tx.OpMu for
// the brief active-tx-table lookup at the top of every method, then
// released BEFORE tx.OpMu is taken. So in practice the manager mutex
// appears at two distinct points in the timeline:
//
//  1. Brief lookup of the tx pointer in the manager's active-tx table.
//     Released immediately. Never held across slow operations.
//  2. Optional re-acquisition INSIDE the OpMu region for committedLog /
//     savepoint-table maintenance, while still holding OpMu.
//
// Holding the manager mutex across the tx.OpMu acquisition is a
// deadlock-bug — Commit holds tx.OpMu while waiting on the manager
// mutex for log maintenance, so any path that holds the manager mutex
// while waiting on tx.OpMu inverts the order.
//
// # Required reading for plugin authors
//
// New methods that touch *TransactionState — on TransactionManager,
// EntityStore, or any other plugin surface — must declare their OpMu
// posture in a code comment ("Locking discipline: ..."). See
// `.claude/rules/tx-state-locking.md` in the cyoda-go-spi repository for
// the review checklist enforced at code review.
type TransactionState struct {
	ID           string
	TenantID     TenantID
	SnapshotTime time.Time
	Origin       Principal // attribution root for the tx; immutable after Begin (see godoc above)
	ReadSet      map[string]bool    // entity IDs read; access under OpMu (see godoc)
	WriteSet     map[string]bool    // entity IDs written; access under OpMu
	Buffer       map[string]*Entity // staged writes; access under OpMu
	Deletes      map[string]bool    // staged deletes; access under OpMu
	RolledBack   bool               // closure flag; written under OpMu.Lock, read under OpMu.RLock
	OpMu         sync.RWMutex       // see TransactionState godoc above for full contract
	Closed       bool               // closure flag; written under OpMu.Lock, read under OpMu.RLock
}

const txContextKey contextKey = "transaction"

// WithTransaction returns a new context carrying the given transaction state.
func WithTransaction(ctx context.Context, tx *TransactionState) context.Context {
	return context.WithValue(ctx, txContextKey, tx)
}

// GetTransaction returns the transaction state from the context, or nil if none.
func GetTransaction(ctx context.Context) *TransactionState {
	tx, _ := ctx.Value(txContextKey).(*TransactionState)
	return tx
}
