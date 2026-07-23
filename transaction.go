package spi

import (
	"context"
	"time"
)

// TransactionManager is the plugin-side surface for the snapshot-isolation
// transaction model. See [TransactionState] for the full concurrency
// contract that implementations must honour.
type TransactionManager interface {
	// Begin starts a new transaction in the caller's tenant. Returns the
	// txID and a child context carrying the new TransactionState. After
	// Begin returns, the TransactionState's immutable fields (ID,
	// TenantID, SnapshotTime) are safe to read without locks.
	Begin(ctx context.Context) (txID string, txCtx context.Context, err error)

	// Commit closes the transaction and applies its buffered writes to
	// the underlying store. Commit acquires tx.OpMu.Lock for its
	// duration, so it waits for any in-flight tx-path operation on the
	// same tx (any SPI method invocation that holds OpMu.RLock) to drain
	// before mutating or closing tx state. Implementations must verify
	// that the caller's tenant matches tx.TenantID and reject
	// mismatched-tenant calls.
	Commit(ctx context.Context, txID string) error

	// Rollback closes the transaction and discards its buffered writes.
	// Acquires tx.OpMu.Lock; same tenant verification as Commit.
	Rollback(ctx context.Context, txID string) error

	// Join returns a context carrying the TransactionState for an existing
	// active transaction, allowing multiple goroutines to participate in
	// the same tx.
	//
	// Two distinct contracts apply to a joined tx:
	//
	//   - Plugin contract (enforced by [TransactionState.OpMu]): the
	//     plugin's tx-path SPI methods hold OpMu.RLock; the plugin's
	//     closure SPI methods (Commit, Rollback, RollbackToSavepoint)
	//     hold OpMu.Lock. So closure waits for any in-flight SPI-method
	//     invocation to return before mutating or closing tx state. This
	//     contract covers SPI-method invocations only — application code
	//     that mutates tx state directly (e.g. through a [GetTransaction]
	//     handle) is outside the OpMu protection.
	//
	//   - Application contract (NOT enforced by the plugin): the
	//     application must serialise its own concurrent in-flight ops on
	//     the same tx. OpMu.RLock allows multiple readers concurrently;
	//     two RLock-holding ops (e.g. two Save calls from different
	//     goroutines) will trigger Go's "concurrent map writes" runtime
	//     fatal because both write to tx.Buffer / tx.WriteSet / tx.Deletes
	//     without mutual exclusion. RLock does not protect map writes from
	//     each other regardless of key overlap. The plugin does not detect
	//     or recover from this contract violation.
	//
	// Implementations must verify that the caller's tenant matches
	// tx.TenantID and reject mismatched-tenant joins. Implementations must
	// read tx.RolledBack and tx.Closed under tx.OpMu.RLock (not under the
	// manager mutex) — Commit's deferred Closed-write runs outside the
	// manager-mutex region.
	Join(ctx context.Context, txID string) (txCtx context.Context, err error)

	GetSubmitTime(ctx context.Context, txID string) (time.Time, error)

	// Savepoint creates a named savepoint within the given transaction by
	// snapshotting tx.Buffer / tx.ReadSet / tx.WriteSet / tx.Deletes, with
	// tx.DeleteAttribution snapshotted paired with tx.Deletes.
	//
	// Locking discipline: read-only on tx state. Implementations must
	// acquire tx.OpMu.RLock for the snapshot read so the operation is
	// serialised against Commit/Rollback (which take tx.OpMu.Lock)
	// without blocking other in-flight readers.
	//
	// Tenant isolation: implementations must reject calls whose
	// UserContext tenant does not match tx.TenantID.
	Savepoint(ctx context.Context, txID string) (savepointID string, err error)

	// RollbackToSavepoint rolls back all work done since the savepoint was
	// created by replacing tx.Buffer / tx.ReadSet / tx.WriteSet /
	// tx.Deletes with the snapshot taken at Savepoint time, restoring
	// tx.DeleteAttribution paired with tx.Deletes.
	//
	// Locking discipline: write on tx state — exclusive against every
	// other tx-path op. Implementations must acquire tx.OpMu.Lock (write
	// lock, not RLock) for the duration of the field replacement.
	//
	// Tenant isolation: implementations must reject mismatched-tenant
	// callers — RollbackToSavepoint is destructive on tx-state.
	RollbackToSavepoint(ctx context.Context, txID string, savepointID string) error

	// ReleaseSavepoint releases a savepoint, merging its work into the
	// parent transaction. The work done since the savepoint already lives
	// in tx.Buffer / tx.ReadSet / tx.WriteSet / tx.Deletes / tx.DeleteAttribution
	// — Release only removes the snapshot record from manager-side state.
	//
	// Locking discipline: does not touch any field of TransactionState
	// (only manager-side savepoint records). Implementations need only
	// the manager mutex; tx.OpMu is not required.
	//
	// Tenant isolation: implementations must reject mismatched-tenant
	// callers — manager-side savepoint state is tenant-scoped.
	ReleaseSavepoint(ctx context.Context, txID string, savepointID string) error
}
