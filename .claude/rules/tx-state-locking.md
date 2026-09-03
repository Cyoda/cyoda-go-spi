---
paths:
  - "**/*.go"
---
# Transaction-State Locking Discipline

Plugin implementations must coordinate concurrent access to
`*spi.TransactionState`'s mutable fields via `tx.OpMu`. The full contract
lives on `TransactionState`'s godoc — this rule is the checklist enforced
at code review.

## When this rule applies

Any new method (or change to an existing method) **on any plugin type** —
`TransactionManager`, `EntityStore`, `SearchStore`, or any other surface —
that reads or writes any of:

- `tx.ReadSet`, `tx.WriteSet`, `tx.Buffer`, `tx.Deletes`
- `tx.RolledBack`, `tx.Closed`

The rule applies to a method based on the fields it touches, not the
interface it implements. Adding a method like `EntityStore.Touch(ctx)`
that mutates `tx.Buffer` is in scope; adding a method that does not
consult `spi.GetTransaction(ctx)` at all (historical reads, aggregate
queries against committed state) is out of scope.

The immutable fields (`tx.ID`, `tx.TenantID`, `tx.SnapshotTime`) do not
require locks once `Begin` has returned.

## Required posture

| Operation class | Lock posture | Examples |
|---|---|---|
| In-flight tx-path operation (read or write on tx state) | `tx.OpMu.RLock` | Save, CompareAndSave, Get, GetPage, Search, Iterate, GetAsAt, Delete, DeleteAll, Exists, Count, Savepoint |
| Closure operation (waits for in-flight to drain) | `tx.OpMu.Lock` | Commit, Rollback, RollbackToSavepoint |
| Tx-state-free, manager-state-only | manager mutex only | ReleaseSavepoint |

## Required code comment

Every method touching `*TransactionState` must declare its OpMu posture in
a `// Locking discipline:` comment block on the method godoc. Example:

```go
// Save persists an entity to the transaction's write buffer.
//
// Locking discipline: holds tx.OpMu.RLock for the duration of the
// tx-state read of tx.RolledBack and the writes to tx.Buffer / tx.WriteSet,
// so Commit/Rollback (which take tx.OpMu.Lock) cannot race with us.
// Lock order: tx.OpMu before factory.entityMu.
func (s *EntityStore) Save(ctx context.Context, e *spi.Entity) (...) {
    // ...
}
```

The comment is mandatory because the lock posture is not visible at the
call site. Reviewers verify it; the comment is the audit trail.

## Required test

Every new method touching `*TransactionState` must include a race-detector
regression test that exercises the new method against the matching
contender (Commit or Rollback) under `go test -race`. See
`plugins/memory/concurrency_*_test.go` in the cyoda-go repo for examples.

If the lock posture pairing is mutually exclusive (e.g. RLock vs Lock on
the same OpMu), the race detector cannot flag it directly — the test
becomes a contract-pin sentinel rather than a race reproducer. Document
the test class in its block comment.

Tolerated-error matching in race tests must use `errors.Is` against
sentinel error types once those land (tracked as #200 in cyoda-go).
Until then, substring matching is acceptable but introduces a subtle
weakness: a future contributor who introduces a new error path whose
message happens to contain a tolerated substring would be silently
swallowed. Mark such tests with `// TODO(#200): replace substring match
with errors.Is` so the cleanup is discoverable.

## Tenant isolation (paired requirement)

Every method that mutates `*TransactionState` must verify
`uc.Tenant.ID == tx.TenantID` and reject mismatched-tenant callers, even
if the locking discipline is correct. The plugin is the data-access
boundary; tenant isolation cannot be deferred upward. Mirror the existing
Commit/Rollback pattern.

## Lock order invariant

Plugin implementations acquire locks in this order to avoid deadlock:

```
tx.OpMu  →  factory's per-store mutex  →  manager's per-tx-table mutex
```

Re-acquiring the manager mutex inside the OpMu region is permitted as long
as the order is preserved. Inverting the order (e.g. acquiring
factory.entityMu before tx.OpMu) is a defect — caught by review or by the
race detector if any code path holds the inverted order.

## Why

Pre-discipline, plugins drifted into races between in-flight ops and
Commit/Rollback. The fix landed in waves as each round of audit surfaced
another method that lacked OpMu coverage:

- cyoda-go PR #153 (v0.6.3) — Save, CompareAndSave.
- cyoda-go #176 / PR #198 — Get, GetAll, Delete, DeleteAll, Exists, Count.
- cyoda-go PR #198 final review — GetAsAt (folded in same PR).
- cyoda-go #199 / PR #201 — Savepoint, RollbackToSavepoint, Join.

The invariant that every TransactionState-touching method declares its
posture in a code comment makes drift visible at review and prevents the
iterative whack-a-mole pattern from continuing. New plugin types and new
methods on existing plugin types are both covered — the rule is scoped
by field access, not by interface membership.
