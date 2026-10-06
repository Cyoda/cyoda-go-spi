package spi

import "errors"

// ErrNotFound indicates the requested resource does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict indicates the write conflicts with a concurrent modification.
var ErrConflict = errors.New("conflict: entity has been modified")

// ErrEpochMismatch indicates the caller's shard epoch is stale relative to
// the cluster view. Retry after refreshing.
var ErrEpochMismatch = errors.New("shard epoch mismatch")

// ErrEntityModelMismatch is returned by Save when the entity's model
// reference differs from the stored entity's. An entity's model is fixed at
// creation: its model name and version never change. A write that would
// change them is rejected rather than silently rewriting which model the
// entity's history belongs to.
var ErrEntityModelMismatch = errors.New("entity model mismatch")

// ErrRetryExhausted indicates the plugin's retry budget for a
// transparently-retried operation was consumed without success.
// Returned by ExtendSchema when CYODA_SCHEMA_EXTEND_MAX_RETRIES
// attempts have completed without success AND the context was not
// cancelled. Callers may choose to retry at a higher level (with
// backoff) or surface the condition to the end user.
//
// Distinct from ErrConflict: ErrConflict means a single attempt hit
// a conflict; ErrRetryExhausted means the plugin exhausted its
// configured retry budget.
var ErrRetryExhausted = errors.New("retry budget exhausted")

// sentinelErr is the unexported error type used to declare sentinels that
// belong in a hierarchy. The Unwrap method makes errors.Is walk to the
// parent sentinel as well as match the leaf, so callers can match either
// the specific condition or its umbrella.
type sentinelErr struct {
	msg    string
	parent error
}

func (e *sentinelErr) Error() string { return e.msg }
func (e *sentinelErr) Unwrap() error { return e.parent }

// ErrTxNotFound indicates that a transaction handle does not refer to a
// known transaction — either the txID never existed, or its state has
// been fully purged. Wraps ErrNotFound so existing
//
//	errors.Is(err, spi.ErrNotFound)
//
// checks on tx-lifecycle paths continue to match.
var ErrTxNotFound = &sentinelErr{msg: "transaction not found", parent: ErrNotFound}

// ErrSavepointNotFound indicates that a savepoint identifier does not
// refer to a known savepoint on the given transaction. Returned by
// RollbackToSavepoint or ReleaseSavepoint when the named savepoint is
// unknown (either never created, already released, or rolled past).
// Wraps ErrNotFound.
var ErrSavepointNotFound = &sentinelErr{msg: "savepoint not found", parent: ErrNotFound}

// ErrTxAborted indicates the transaction was aborted by an earlier conflict;
// every later statement other than Commit that the engine refuses fails with
// this error, until the transaction ends. A rollback to a savepoint taken
// before the conflict makes statements run again but does not undo the
// conflict: a conflict anywhere in the transaction is a conflict of the
// transaction, and Commit refuses it with the recorded cause, which is also
// ErrConflict.
// It wraps ErrConflict, so a caller that answers a conflict keeps doing so.
//
// It exists for the caller that must tell a conflict about its own statement
// apart from one that happened earlier: a compare-and-save that fails with
// ErrTxAborted did not find its precondition false — it never ran, because a
// concurrent writer had already won against this transaction. Returned only
// by backends whose engine aborts the whole transaction on a conflict; a
// backend that detects conflicts at commit never returns it.
var ErrTxAborted = &sentinelErr{msg: "transaction aborted by an earlier conflict", parent: ErrConflict}

// ErrTxTerminated is the umbrella sentinel for any operation on a
// transaction that has reached a terminal state (committed or rolled
// back). Callers that do not need to distinguish rollback from commit
// can match this directly.
//
// NOTE: Backends that delegate transaction state to an external engine
// may surface mid-op rollback as ErrConflict — ErrTxAborted when the abort
// was a conflict (e.g. a SQLSTATE 25P02 from a SQL engine after a 40001) —
// instead of ErrTxRolledBack, where the
// engine's abort code is already semantically meaningful. The
// ErrTxTerminated sentinel is required only on plugins that own their
// own in-process tx-state buffer. Consumers writing backend-agnostic
// code should match both ErrTxTerminated and ErrConflict on data-op
// paths.
var ErrTxTerminated = errors.New("transaction in terminal state")

// ErrTxRolledBack indicates that an in-flight operation observed the
// transaction marked rolled-back, or that an op was attempted on a
// transaction whose terminal state is Rollback. Surfaced by plugins
// that own their own in-process tx-state buffer; see ErrTxTerminated
// godoc for the alternate-surface caveat on plugins that delegate
// transaction state to an external engine.
var ErrTxRolledBack = &sentinelErr{msg: "transaction rolled back", parent: ErrTxTerminated}

// ErrTxAlreadyCommitted indicates an attempt to Join, Commit, or
// otherwise operate on a transaction whose terminal state is Commit.
// Wraps ErrTxTerminated.
var ErrTxAlreadyCommitted = &sentinelErr{msg: "transaction already committed", parent: ErrTxTerminated}

// ErrTxCommitInProgress indicates that Commit was called on a
// transaction another goroutine is already committing. Distinct from
// ErrTxTerminated because the transaction is not yet terminal — the
// loser of the race may still observe the committed result.
//
// Note: this sentinel has no portable conformance subtest in spitest
// because reliably racing two Commit goroutines from a black-box harness
// is brittle. Backends that own their own in-process commit registry
// exercise this in their internal concurrency suites.
var ErrTxCommitInProgress = errors.New("transaction commit in progress")

// ErrTxNotCommitted indicates that GetSubmitTime was called on a
// transaction that exists and is still in flight. Distinct from
// ErrTxNotFound: the transaction is known, so answering "not found"
// would be a wrong definitive answer; and distinct from ErrTxTerminated,
// because it has not reached a terminal state.
//
// It exists so a caller can tell this apart from an unexpected failure.
// Every backend reported it as an unsentinelled error, which left an
// HTTP door unable to distinguish a live transaction from a storage
// fault and classifying both as caller error.
//
// Tenant isolation: a caller whose tenant does not own the transaction
// gets ErrTxTenantMismatch instead — this sentinel must never be the
// answer to a cross-tenant lookup, or it becomes an existence oracle.
var ErrTxNotCommitted = errors.New("transaction not yet committed")

// ErrConsistencyTimeUnavailable is returned (wrapped) by
// TransactionManager.ConsistencyTime when the store cannot certify a
// consistency time within its wait budget — typically because a save of the
// tenant is held in its commit phase. It is transient: a retry may succeed.
var ErrConsistencyTimeUnavailable = errors.New("consistency time unavailable")

// ErrTxTenantMismatch indicates a tenant mismatch against the transaction
// on ctx:
//
//   - A transaction-lifecycle operation (Join, Commit, Rollback,
//     Savepoint, etc.) was attempted with a UserContext whose tenant does
//     not match the transaction's tenant.
//   - A JOINING ScheduledTaskStore write (see ScheduledTaskStore's doc)
//     was called with a transaction on ctx whose tenant is not the
//     method's own tenant argument (or req.TenantID / ref.TenantID).
//
// Tenant-isolation invariant in both cases.
var ErrTxTenantMismatch = errors.New("transaction tenant mismatch")

// ErrGroupCardinalityExceeded is returned by GroupedAggregator
// implementations (or surfaced by the service-layer streaming tally)
// when the result group count would exceed the configured ceiling.
var ErrGroupCardinalityExceeded = errors.New("group cardinality exceeded ceiling")

// ErrSearchResultLimitExceeded is returned by an EntityStore whose direct
// search (EntityStore.Search) matched more entities than the configured
// result-limit cap (bounded-or-fail contract). The engine maps it to a
// client-facing 400.
var ErrSearchResultLimitExceeded = errors.New("search result limit exceeded")

// ErrAggregationNotPushdownable signals that a GroupedAggregator
// implementation cannot safely push down a specific request shape; the
// caller (typically the service layer) should fall through to the
// streaming-tally path via EntityStore.Iterate.
var ErrAggregationNotPushdownable = errors.New("aggregation request shape not pushdownable")

// ErrUniqueViolation: a write would duplicate a declared composite unique key.
// Deterministic, NON-retryable (distinct from ErrConflict).
var ErrUniqueViolation = errors.New("composite unique key violation")

// ErrPartialUniqueKey is the umbrella for every ComputeClaims VALUE-invalid
// error — a partially-filled key, an over-bound numeric literal, or a
// non-scalar value at a key path. All map to 422 INVALID_UNIQUE_KEY.
var ErrPartialUniqueKey = errors.New("invalid composite unique key value")

// ErrAlreadyTerminal is returned by AsyncSearchStore write methods
// (UpdateJobStatus, Heartbeat, SaveResults, Release, ClearResults) called
// against a job already in a terminal status (SUCCESSFUL/FAILED/CANCELLED).
// Cancel is the sole idempotent-nil exception.
var ErrAlreadyTerminal = errors.New("job is in a terminal status")

// ErrStaleClaim is returned by a fenced write whose caller no longer holds
// the claim it names:
//
//   - AsyncSearchStore (UpdateJobStatus, SaveResults, Heartbeat, Release,
//     ClearResults): the caller's epoch does not match the job's current
//     Epoch — another claimant has since taken over.
//   - ScheduledTaskStore (StampSegment, MarkUnsafe, RecordAttempt, Fail):
//     the task is missing, or its current arm token or claim token is not
//     the one in the TaskRef — the task was re-armed, reclaimed, recorded,
//     failed or removed since the caller claimed it.
var ErrStaleClaim = errors.New("write fenced: stale claim epoch")

// ErrMarkedByAnotherClaim is returned by ScheduledTaskStore.MarkUnsafe when
// an earlier claim of the same life already wrote a mark. Work that is not
// safe to repeat may already have been handed off for this life, so the
// caller must not dispatch it again.
var ErrMarkedByAnotherClaim = errors.New("scheduled task: marked by another claim of this life")

// ErrTaskBusy is returned by ScheduledTaskStore.MarkUnsafe and
// ScheduledTaskStore.RecordAttempt when an open transaction has written the
// task row. The write is not made. The caller treats it as a failure that
// is safe to retry.
//
// AsyncSearchStore.Heartbeat may answer it too, when the job's row is held
// by an open write of the same job (for example a SaveResults chunk's
// fencing transaction); Heartbeat does not wait and makes no write, and the
// caller treats the answer as a missed tick, not a lost claim.
//
// ScheduledTaskStore.ClaimDue may answer it too, on a backend that
// lock-waits rather than selecting candidates without blocking: a bounded
// wait for a lock unrelated to the MarkUnsafe/ClaimDue race (C3) that gives
// up is reported this way for the call, instead of blocking indefinitely.
// This is distinct from a row an open transaction has written (C6), which
// ClaimDue always just skips, never waiting and never erroring for it.
//
// The sentinel is store-neutral: it names no store because more than one
// now returns it.
var ErrTaskBusy = errors.New("row is being written by an open transaction")

// ErrStoreRejected marks a deterministic rejection by the store: the same
// write with the same input fails the same way every time, so retrying it
// cannot succeed. Examples: input that breaks a documented precondition
// (such as Attempt.Error over 1024 bytes, not valid UTF-8, or holding a
// NUL), or a constraint or data error from the database (on PostgreSQL,
// SQLSTATE classes 22, 23 and 42). A store wraps such an error so that
//
//	errors.Is(err, spi.ErrStoreRejected)
//
// holds. This applies to every method of every store in this SPI, and to a
// transaction's Commit — including StateMachineAuditStore.Record, which the
// engine calls in the same transaction as ScheduledTaskStore.Fail. Every
// other failure — outage, timeout, lock wait, pool exhaustion, conflict —
// must NOT carry it: callers retry those.
var ErrStoreRejected = errors.New("store rejected the write deterministically")

// ErrUnknownOperator is returned by ConditionToFilter for a condition leaf
// whose operatorType is not in the closed set OperatorNames reports.
//
// It is a distinct sentinel because it means the INPUT is invalid, which a
// caller should surface as a client error (400 INVALID_CONDITION). Other
// translation failures mean the predicate is well-formed but not expressible
// as a pushdown Filter, which is a different answer entirely.
var ErrUnknownOperator = errors.New("unknown condition operator")

// ErrInvalidPattern is returned by [ValidateLeafPattern] and
// [ValidateConditionPatterns] for an operand that cannot be used as a pattern:
// a LIKE operand ending in an unpaired escape, or a MATCHES_PATTERN operand
// that does not compile.
//
// The wrapped message names the operator and the failure, and deliberately
// carries NEITHER the operand NOR the anchored form the kernel compiles — a
// caller puts this error into a client-facing 400, and both are internals.
var ErrInvalidPattern = errors.New("invalid pattern")

// ErrInvalidFilterPath is returned for a Filter.Path — or an OrderSpec.Path —
// that falls outside the documented path grammar. See the "Grammar" and
// "Rejection is mandatory" sections of [Filter]'s Path field: a non-empty path
// is a dotted run of ASCII identifier segments, and a backend MUST refuse
// anything else with an error rather than answering with an empty result set.
//
// [ConditionToFilter] also returns it one step earlier, for the WIRE form: a
// condition jsonPath that is not JSON Path nomenclature — no "$." leader, an
// empty or trailing segment, bracket-quoted access, a bracket spelling
// outside the two supported subscript forms (the wildcard "[*]" and a
// non-negative index that fits an int32), or any other disallowed character.
// Note what it does NOT cover there: a WELL-FORMED array-subscripted path
// ("$.tags[*]", "$.arr[0]") is not invalid input at all — it translates like
// any other well-formed path, because the kernel resolves a subscripted path
// directly (see [ResolvePath]) rather than falling back to in-memory
// evaluation.
//
// Like ErrUnknownOperator this means the INPUT is invalid, so a caller should
// surface it as a client error rather than a storage failure. Backends declare
// their own package-level sentinel of the same name for their local callers;
// each one wraps this, so
//
//	errors.Is(err, spi.ErrInvalidFilterPath)
//
// is the backend-agnostic way to classify a malformed path.
var ErrInvalidFilterPath = errors.New("invalid filter path")

// ErrUnevaluableLeaf is returned by [Prepare] for a Filter leaf it cannot
// evaluate: an operand that parses into none of the leaf's declared types
// (including an empty/nil declared set), a SourceData leaf whose Path is
// empty or falls outside the documented path grammar, a SourceMeta leaf
// whose Path is empty or is not one of the names extractFilterMetaValue
// recognizes (the plugin-facing meta keyset — broader than the client-facing
// [MetaFieldNames] vocabulary, since it also carries the storage-key aliases
// e.g. "entity_id", "created_at"), a pattern operand (LIKE / MATCHES_PATTERN)
// that will not compile, or an unsupported operator.
//
// It also covers a malformed [FilterNot] node: one whose Children is not
// exactly length 1. That is not a leaf defect, but the same umbrella applies
// for the same reason — it is a property of the request, decided once at
// prepare time — and FilterNot's single child is itself prepared through this
// same recursion, so a zero-Op or otherwise-unevaluable child surfaces this
// sentinel too, one level down.
//
// Every cause is decided at prepare time, from the condition alone, before
// any entity is read — it is a property of the REQUEST, not an artifact of a
// particular row. Prepare therefore rejects the whole filter rather than
// silently building a leaf that never matches: a leaf that never matches is
// safe only in the absence of negation, because a NOT would invert it into
// matches-everything.
//
// The "no declared type" wrapped message names the operand but caps it at
// [maxEchoedOperandBytes] (see truncateOperand): this is the ONLY
// documented-normal case here — a field with no declared type on a search
// request — so it is also the only one reachable with a caller-sized
// (megabyte-scale) operand; echoing it verbatim would let an ordinary 400
// blow up to request size, and it is logged again as "cause" by callers.
var ErrUnevaluableLeaf = errors.New("unevaluable leaf")
