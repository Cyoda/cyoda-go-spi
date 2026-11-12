package spi

import (
	"context"
	"errors"
)

// TransactionCoordinationCapable is OPTIONAL on a StoreFactory, in the
// pattern of CompositeUniqueKeyCapable. A factory that implements it has
// location-independent transactions.
//
// TransactionCoordinator returning a nil coordinator and a nil error means
// the factory is not capable. A wrapper that forwards the capability by hand
// returns exactly that when the factory it wraps does not implement the
// interface. cyoda-go calls the method once at start-up, and uses its own
// in-process coordination when the coordinator is nil.
//
// A transaction of a capable store may be served by any cyoda-go node: Join
// on it allows concurrent operations on one transaction from any number of
// goroutines and nodes (see TransactionManager.Join).
type TransactionCoordinationCapable interface {
	TransactionCoordinator(ctx context.Context) (TransactionCoordinator, error)
}

// CalloutPair names one callout at one fencing number. The number (Major,
// Minor) of a callout only rises.
type CalloutPair struct {
	Callout string
	Major   uint32
	Minor   uint32
}

// TransactionCoordinator coordinates the work done in one transaction by its
// owner and by the callbacks of the callouts the owner makes. Every node that
// serves the transaction uses the same coordination state.
//
// # Callouts and scopes
//
// A callout is one call from the transaction's work to a compute node. A
// callback of that callout is a request that joins the transaction under a
// pass: the pass names the callout first, then every callout that encloses
// it, each with the number the pass was issued under (a []CalloutPair).
//
// A scope is the unit of all-or-nothing work in a transaction. The scopes of
// a transaction form a tree:
//
//   - the root scope is the owner's own work. A context that carries the
//     transaction and no scope (the context Begin returned) is on the root;
//   - a callback scope is one joined request, opened by OpenScope;
//   - a child scope is opened by OpenChildScope as a child of the scope on
//     the context it is given.
//
// A callout is registered with the scope on the context given to
// BeginCallout. The parent of a callback scope is the scope its first pair's
// callout is registered with.
//
// # Ownership of writes
//
// Every tx-path write made on a scope's context belongs to that scope.
//
// # Completion and discard
//
// CompleteScope makes the scope's writes part of its parent scope. A later
// discard of the parent, or of any ancestor, removes them. DiscardScope
// removes the scope's writes and those of every scope under it.
//
// # Visibility
//
// A completed scope's writes are visible to later reads of its parent and of
// the parent's later scopes, from any node.
//
// # Fencing numbers
//
// A pair is current when its callout is registered and not ended, its major
// equals the registered major, and its minor is not lower than the highest
// minor seen for the callout. A callout registered by BeginCallout has no
// current pair until its first AdvanceCallout.
//
// # Rollback-only
//
// A scope is marked (rollback-only) by a DiscardScope of one of its children
// with a non-empty failedCode, the code that names the failure: for a child
// whose completion was refused for a mark, the refusal's FailedCode;
// otherwise the code the child's failed request was answered with. The
// coordinator records the code of a scope's first mark; a later mark of the
// same scope keeps it. A marked scope cannot complete. The
// transaction is rollback-only when the root is marked.
//
// A coordinator must never let Seal succeed after a mark it failed to
// record. If DiscardScope with a non-empty failedCode cannot record the mark,
// the coordinator must make the transaction's Seal fail: for example by
// keeping the scope unresolved so that Seal fails, or by having Seal wait
// until the scope's lease expires and then treat it as rollback-only.
//
// Every refusal for a mark (by OpenScope, CheckScope, CompleteScope or Seal)
// is a *RollbackOnlyError, which matches ErrTxRollbackOnly under errors.Is.
// Its FailedCode is the code recorded for the nearest marked scope on the
// chain the refusal checks: for CheckScope and CompleteScope the scope itself
// or, when it is not marked, its nearest marked ancestor; for OpenScope the
// parent or its nearest marked ancestor; for Seal the root.
//
// # Concurrency
//
// Every method may be called concurrently, from any number of goroutines and
// nodes, for the same transaction.
type TransactionCoordinator interface {
	// BeginCallout registers callout calloutID of transaction txID with the
	// scope on ctx. outer names the enclosing callouts, as the pass of the
	// scope on ctx names them. It refuses with ErrCalloutSuperseded when an
	// outer pair is not current.
	//
	// Nested callouts on another node. Nothing cancels a nested callout
	// running on another node when its enclosing scope is superseded or
	// marked. The owner's loop for that nested callout calls CheckScope on
	// its enclosing scope before each try and each hand-over, and stops when
	// it fails. A try already in progress runs until it is answered or
	// reaches its time limit; its scope cannot complete either way.
	BeginCallout(ctx context.Context, txID, calloutID string, outer []CalloutPair) error

	// AdvanceCallout raises the callout's major to major, with minor 0.
	// After it returns, no scope admitted under the callout at a major below
	// major can complete. It does not wait for scopes in progress: such a
	// scope ends discarded by its own CompleteScope, which refuses with
	// ErrCalloutSuperseded.
	AdvanceCallout(ctx context.Context, txID, calloutID string, major uint32) error

	// EndCallout ends the callout. After it returns, no scope admitted under
	// it can complete or open. It does not wait for scopes in progress: such
	// a scope ends discarded by its own CompleteScope, which refuses with
	// ErrCalloutSuperseded.
	EndCallout(ctx context.Context, txID, calloutID string) error

	// OpenScope opens a callback scope of transaction txID for a request
	// whose pass carries pairs (its own callout first, then every enclosing
	// one), and returns the context the request runs on.
	//
	// It refuses with ErrCalloutSuperseded unless every pair is current. In
	// the same atomic step it raises a callout's highest seen minor to the
	// pair's minor when that is higher, so a pass refused for any pair
	// changes nothing. It refuses with a *RollbackOnlyError when the parent
	// or an ancestor is marked. After Seal it refuses with
	// ErrCalloutSuperseded.
	OpenScope(ctx context.Context, txID string, pairs []CalloutPair) (context.Context, error)

	// OpenChildScope opens a scope as a child of the scope on ctx, and
	// returns the context its work runs on. cyoda-go opens one for each
	// ASYNC_NEW_TX processor. Its callouts are registered with it by
	// BeginCallout on the returned context.
	OpenChildScope(ctx context.Context) (context.Context, error)

	// CheckScope reports ErrCalloutSuperseded and the *RollbackOnlyError for
	// the scope on ctx as CompleteScope would, without completing it.
	CheckScope(ctx context.Context) error

	// CompleteScope makes the writes of the scope on ctx part of its parent.
	// It refuses, in this order:
	//
	//   - ErrCalloutSuperseded when a pair of the scope is not current, or
	//     the transaction is sealed;
	//   - a *RollbackOnlyError when the scope or an ancestor is marked;
	//   - ErrScopeConflict when a completed scope of the same transaction
	//     wrote an entity this scope wrote, from the same version.
	//
	// A refused completion discards the scope.
	CompleteScope(ctx context.Context) error

	// DiscardScope removes the writes of the scope on ctx and those of every
	// scope under it. With failedCode "" it marks nothing. A non-empty
	// failedCode is the code that names the failure: for a scope whose
	// completion was refused for a mark, the refusal's FailedCode; otherwise
	// the code the scope's failed request was answered with. With it,
	// DiscardScope also marks the scope's parent and records the code for
	// it, unless the parent is already marked: the first mark's code stands.
	//
	// DiscardScope of a scope whose completion was refused is allowed. The
	// refused completion has already discarded the scope, so DiscardScope
	// removes nothing more: no write made since, by the transaction or by any
	// other scope, is removed. With a non-empty failedCode it still marks the
	// parent.
	//
	// A DiscardScope error does not release the coordinator from recording
	// the mark: if it could not record it, Seal of the transaction must still
	// fail (see "Rollback-only" on TransactionCoordinator).
	//
	// The caller does not call DiscardScope after a successful CompleteScope
	// of the same scope; a coordinator may refuse such a call.
	DiscardScope(ctx context.Context, failedCode string) error

	// Seal is the owner's cut-off before commit or rollback. It fails with a
	// *RollbackOnlyError, carrying the root's code, when the root is marked,
	// including a mark that DiscardScope failed to record.
	// After it returns, OpenScope and CompleteScope both refuse with
	// ErrCalloutSuperseded. Commit after a successful Seal commits the root
	// and every completed scope.
	Seal(ctx context.Context, txID string) error
}

var (
	// ErrCalloutSuperseded: a pair is no longer current (its callout
	// advanced past it or ended), or the transaction is sealed.
	ErrCalloutSuperseded = errors.New("callout superseded")
	// ErrTxRollbackOnly: the scope, an ancestor, or the transaction's root
	// is marked rollback-only. A coordinator refuses with a
	// *RollbackOnlyError, which matches it under errors.Is.
	ErrTxRollbackOnly = errors.New("transaction is rollback-only")
	// ErrScopeConflict: a completed scope of the same transaction wrote an
	// entity this scope wrote, from the same version.
	ErrScopeConflict = errors.New("scope conflicts with another scope of the transaction")
)

// RollbackOnlyError is a coordinator's refusal for a mark. FailedCode is the
// code recorded for the nearest marked scope on the chain the refusal checks
// (see "Rollback-only" on TransactionCoordinator). It matches
// ErrTxRollbackOnly under errors.Is; find it with errors.As on a
// *RollbackOnlyError.
type RollbackOnlyError struct {
	FailedCode string
}

func (e *RollbackOnlyError) Error() string {
	return ErrTxRollbackOnly.Error() + " (failed with " + e.FailedCode + ")"
}

// Is reports whether target is ErrTxRollbackOnly.
func (e *RollbackOnlyError) Is(target error) bool {
	return target == ErrTxRollbackOnly
}
