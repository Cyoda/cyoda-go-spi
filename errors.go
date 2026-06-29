package spi

import "errors"

// ErrNotFound indicates the requested resource does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict indicates the write conflicts with a concurrent modification.
var ErrConflict = errors.New("conflict: entity has been modified")

// ErrEpochMismatch indicates the caller's shard epoch is stale relative to
// the cluster view. Retry after refreshing.
var ErrEpochMismatch = errors.New("shard epoch mismatch")

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

// ErrTxTerminated is the umbrella sentinel for any operation on a
// transaction that has reached a terminal state (committed or rolled
// back). Callers that do not need to distinguish rollback from commit
// can match this directly.
//
// NOTE: Backends that delegate transaction state to an external engine
// may surface mid-op rollback as ErrConflict (e.g. via a SQLSTATE
// 25P02 from a SQL engine) instead of ErrTxRolledBack, where the
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

// ErrTxTenantMismatch indicates a transaction-lifecycle operation
// (Join, Commit, Rollback, Savepoint, etc.) was attempted with a
// UserContext whose tenant does not match the transaction's tenant.
// Tenant-isolation invariant — distinct from data-op tenant checks.
var ErrTxTenantMismatch = errors.New("transaction tenant mismatch")

// ErrGroupCardinalityExceeded is returned by GroupedAggregator
// implementations (or surfaced by the service-layer streaming tally)
// when the result group count would exceed the configured ceiling.
var ErrGroupCardinalityExceeded = errors.New("group cardinality exceeded ceiling")

// ErrAggregationNotPushdownable signals that a GroupedAggregator
// implementation cannot safely push down a specific request shape; the
// caller (typically the service layer) should fall through to the
// streaming-tally path via Iterable.
var ErrAggregationNotPushdownable = errors.New("aggregation request shape not pushdownable")

// ErrUniqueViolation: a write would duplicate a declared composite unique key.
// Deterministic, NON-retryable (distinct from ErrConflict).
var ErrUniqueViolation = errors.New("composite unique key violation")

// ErrPartialUniqueKey is the umbrella for every ComputeClaims VALUE-invalid
// error — a partially-filled key, an over-bound numeric literal, or a
// non-scalar value at a key path. All map to 422 INVALID_UNIQUE_KEY.
var ErrPartialUniqueKey = errors.New("invalid composite unique key value")
