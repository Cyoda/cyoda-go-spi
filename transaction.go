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
	// active transaction. Multiple goroutines may participate in the same
	// tx, but only one operation at a time per transaction:
	// application-side serialisation is required, per the Application
	// contract below and [TransactionState]'s concurrency contract
	// (cyoda-go serialises through its per-transaction gate).
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

	// GetSubmitTime returns the instant the transaction committed. It is the
	// same instant stamped on every row that transaction wrote, and it must
	// be answerable by any node, not only the one that committed.
	GetSubmitTime(ctx context.Context, txID string) (time.Time, error)

	// ConsistencyTime returns the consistency time C for the tenant in ctx:
	// an instant, in the store's own stamp domain, with four properties.
	//
	//   - Complete: every save, of any tenant, whose success was returned on
	//     any node before this call started has a stamp <= C.
	//   - Final: a read for this tenant at T <= C that starts after this call
	//     returned sees every save of this tenant stamped <= T, now and later.
	//     A save not yet stamped when C is returned is stamped > C.
	//   - Monotonic: every C returned, for any tenant on any node, is >= every
	//     C returned before this call started, across restarts too.
	//   - Read resolution: if the store widens an instant to a coarser unit
	//     when it reads (a whole millisecond, say), C closes that whole unit.
	//
	// The mechanism is "reserve, then wait": raise the stamp floor to
	// max(store clock, highest stamp issued), then wait until every save of
	// the tenant already holding a stamp <= C has committed or aborted.
	//
	// It never returns a guessed instant. When the store cannot certify C
	// within its own wait budget it returns an error wrapping
	// ErrConsistencyTimeUnavailable. It never uses, joins or holds the
	// transaction in ctx, so it is safe to call from inside one.
	ConsistencyTime(ctx context.Context) (time.Time, error)

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
	// It does not undo a conflict the backend already detected after the
	// savepoint: the transaction lost a race, and Commit refuses it with that
	// conflict (see ErrTxAborted). The same holds for a backend that detects
	// conflicts at commit: a write the rollback discards that has already
	// lost first-committer-wins — another transaction committed that entity
	// or task row after this transaction's snapshot and before the rollback
	// — makes Commit refuse the transaction with ErrConflict. A commit to
	// that entity or task row after the rollback does not, because the
	// transaction no longer writes it. Read-set entries the rollback
	// discards are dropped.
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

	// LostRace reports whether the transaction has already lost a write
	// race: another transaction committed, after this transaction's
	// snapshot, an entity or task row that this transaction writes. Such a
	// transaction cannot commit — Commit refuses it with ErrConflict — so a
	// caller that sees a failure inside it can tell, before it commits, that
	// the conflict is what happened and the failure is only a consequence.
	// The answer is the same on every backend:
	//
	//   - A backend whose engine aborts the transaction at the losing write
	//     (see ErrTxAborted) answers true once that conflict is recorded,
	//     including after a rollback to a savepoint taken before it.
	//   - A backend that detects conflicts at commit answers true when a
	//     transaction that committed after this one's snapshot wrote an
	//     entity or task row this transaction writes, or when a savepoint
	//     rollback discarded a write that had already lost (see
	//     RollbackToSavepoint). It is the check Commit makes on writes.
	//
	// It answers false when no rival has committed: a rival that has written
	// but not committed has not won, and whichever transaction commits first
	// wins. The one exception is a deadlock: a backend whose engine aborts
	// the victim of a deadlock between two open transactions (PostgreSQL's
	// 40P01) answers true for the victim, whose rival has not committed,
	// because Commit refuses the victim with ErrConflict all the same. A
	// rival that committed before this transaction's snapshot raced nothing.
	// A change committed to an entity the transaction only read is not a
	// lost write race; Commit's read-set validation refuses it. Once true,
	// the answer stays true until the transaction ends.
	//
	// LostRace changes nothing, and it must answer on a transaction whose
	// engine has aborted it, without error: it issues no statement that the
	// aborted transaction would refuse.
	//
	// Locking discipline: read-only on tx state. Implementations that read
	// tx.WriteSet acquire tx.OpMu.RLock, as Savepoint does.
	//
	// Tenant isolation: implementations must reject calls whose UserContext
	// tenant does not match tx.TenantID with ErrTxTenantMismatch, and answer
	// ErrTxNotFound for a transaction they do not know.
	LostRace(ctx context.Context, txID string) (bool, error)
}
