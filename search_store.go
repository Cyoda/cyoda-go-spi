package spi

import (
	"context"
	"encoding/json"
	"iter"
	"time"
)

// SearchJob represents the persistent state of an async search operation.
type SearchJob struct {
	ID       string
	TenantID TenantID
	Status   string // RUNNING, SUCCESSFUL, FAILED, CANCELLED
	ModelRef ModelRef

	// Condition is the client's predicate in the DOMAIN wire syntax
	// ([predicate.Condition] as JSON), deliberately NOT translated to a
	// [Filter]. It is the one plugin-facing field that carries domain syntax;
	// every other predicate surface here (EntityStore.Search,
	// EntityStore.Iterate, GroupedAggregate) takes a Filter.
	//
	// For a store the engine executes, this field is OPAQUE: persist it and
	// return it unchanged. The engine reads it back and translates it itself.
	//
	// For a [SelfExecutingSearchStore] it is the input to execution, and the
	// obligations on that interface apply — translate it with
	// [ConditionToFilter], do not parse or evaluate it independently.
	//
	// The shape is settled and permanent. Carrying a translated Filter here
	// instead was considered and rejected: [ConditionToFilter] and
	// [FieldsMapFromSchema] already live in this module, so a self-executing
	// store can translate with the kernel's own code, which is what actually
	// prevents divergence. Moving the translation to submission time would
	// also have to define what happens when a condition does not translate,
	// at the one point the engine has already stepped out.
	Condition json.RawMessage

	PointInTime time.Time
	SearchOpts  json.RawMessage
	ResultCount int
	Error       string
	CreateTime  time.Time
	FinishTime  *time.Time
	CalcTimeMs  int64

	// HeartbeatTime is the last liveness stamp from the owning executor.
	// nil means never stamped, in which case staleness is measured from
	// CreateTime (the baseline).
	HeartbeatTime *time.Time

	// Epoch is the claim/attempt counter. CreateJob persists 1 regardless
	// of the value set on the input job; ClaimStale increments it on each
	// successful claim. Callers fence writes (UpdateJobStatus, SaveResults,
	// Heartbeat) against the Epoch they were claimed with.
	Epoch int64
}

// SelfExecutingSearchStore is implemented by AsyncSearchStore variants whose
// CreateJob method also kicks off per-shard execution and result persistence.
// The domain SearchService detects this via a type assertion after CreateJob
// and skips its own background-execution goroutine for these stores.
//
// Memory and Postgres do NOT implement this — their CreateJob only persists
// the job row, and the SearchService spawns a background goroutine to perform
// the actual search. A backend with native distributed execution can opt in
// by implementing this interface; its CreateJob is expected to dispatch work
// and persist results itself.
//
// Self-executing stores may reject SaveResults (they persist results as a
// side effect of CreateJob's own dispatch, not via a caller-driven stream)
// and no-op Heartbeat, ClaimStale, and ClearResults — liveness and reclaim
// are meaningless for a store that owns execution outright.
//
// # Predicate obligation
//
// This is the ONLY interface for which [SearchJob.Condition] is load-bearing:
// an engine-executed store persists that field and never reads it, while a
// self-executing store must act on it with no engine present.
//
// Such a store MUST derive its predicate through this module —
// [FieldsMapFromSchema] over the model schema, then [ConditionToFilter], then
// [Prepare] / [PreparedFilter.Match] — and MUST NOT ship its own condition
// parser or leaf comparator. A second implementation of either is not a local
// choice: it silently answers the same query differently from every other
// backend, and it has already happened once, diverging on numeric precision,
// BETWEEN inclusivity, absent-field handling for negative operators, pattern
// anchoring and array comparison.
//
// Passing a nil or partial fields map does not satisfy this. An empty
// declared-type set does not degrade uniformly — comparison leaves annihilate
// while string and presence leaves evaluate normally — so the result is
// internally inconsistent rather than empty. See [ConditionToFilter].
type SelfExecutingSearchStore interface {
	AsyncSearchStore
	SelfExecuting()
}

// AsyncSearchStore provides persistence for async search jobs and their
// results.
//
// Terminal statuses (SUCCESSFUL/FAILED/CANCELLED) are write-once: once a job
// reaches one, UpdateJobStatus, Heartbeat, and SaveResults against it return
// ErrAlreadyTerminal (SaveResults checks this at least at chunk boundaries).
// Cancel is the sole idempotent-nil exception — cancelling an already-terminal
// job returns nil and leaves it unchanged. ClaimStale never claims a terminal
// job.
//
// Epoch fencing: UpdateJobStatus, SaveResults, and Heartbeat each take the
// epoch the caller was claimed under and MUST refuse a call whose epoch does
// not match the job's current Epoch with ErrStaleClaim — this is how a
// reclaimed job fences off writes from the executor it was taken from.
type AsyncSearchStore interface {
	// CreateJob persists a new job row. Epoch is always persisted as 1,
	// regardless of the value set on job.Epoch by the caller.
	CreateJob(ctx context.Context, job *SearchJob) error

	GetJob(ctx context.Context, jobID string) (*SearchJob, error)

	// UpdateJobStatus fences on epoch (ErrStaleClaim on mismatch) and refuses
	// a terminal job (ErrAlreadyTerminal). Against a missing job it returns
	// ErrNotFound. A zero finishTime is stored as absent (NULL/nil).
	UpdateJobStatus(ctx context.Context, jobID string, epoch int64, status string, resultCount int, errMsg string, finishTime time.Time, calcTimeMs int64) error

	// SaveResults streams entityIDs into the job's persisted result set.
	// Exactly one call is made per claim epoch; yield order is preserved as
	// GetResultIDs order. Implementations batch internally as they see fit,
	// but the result sequence position must increase strictly across chunks.
	// The store observes ctx cancellation. A nil return means everything
	// yielded was durably stored — it is NOT a statement about job success,
	// which is recorded separately via UpdateJobStatus.
	//
	// Fences on epoch (ErrStaleClaim) and terminal status (ErrAlreadyTerminal,
	// checked at least at chunk boundaries), matching UpdateJobStatus. A
	// missing job returns ErrNotFound.
	//
	// The fence is a property of the CALL, not of the rows it carries: an
	// entityIDs sequence that yields nothing MUST still be fenced, and MUST
	// still report ErrStaleClaim / ErrAlreadyTerminal / ErrNotFound where a
	// non-empty sequence would have. A search matching zero entities is an
	// ordinary outcome, so short-circuiting on "nothing to write" is exactly
	// the case where a reclaimed executor is most likely to learn — or fail to
	// learn — that it has been fenced off.
	SaveResults(ctx context.Context, jobID string, epoch int64, entityIDs iter.Seq[string]) error

	// GetResultIDs requires offset >= 0 && limit >= 1; a violation returns an
	// error, never a panic. Reading a non-terminal job answers with the
	// results saved so far.
	GetResultIDs(ctx context.Context, jobID string, offset, limit int) (entityIDs []string, total int, err error)

	DeleteJob(ctx context.Context, jobID string) error

	// ReapExpired deletes eligible expired jobs. Cross-tenant: obtain with a
	// background/tenant-less context, as with ScheduledTaskStore.ScanDue
	// (persistence.go:19-24).
	ReapExpired(ctx context.Context, ttl time.Duration) (int, error)

	// Cancel marks the job CANCELLED and stamps the given finishTime on the
	// job it transitions. Idempotent: cancelling a job already in a
	// terminal state returns nil AND does not overwrite the existing finish
	// time. Cancelling a non-existent job returns ErrNotFound. The finish
	// time is caller-supplied so all backends record the same instant — the
	// engine is the single clock.
	Cancel(ctx context.Context, jobID string, finishTime time.Time) error

	// Heartbeat stamps HeartbeatTime, fenced by epoch. Returns ErrStaleClaim
	// if epoch does not match the job's current Epoch, ErrAlreadyTerminal if
	// the job is already terminal, and ErrNotFound if the job does not exist.
	Heartbeat(ctx context.Context, jobID string, epoch int64) error

	// ClaimStale atomically claims up to limit RUNNING jobs whose heartbeat
	// (HeartbeatTime, or CreateTime as the baseline when HeartbeatTime is
	// nil) is older than staleAfter. It never claims a terminal job. Claiming
	// bumps Epoch and stamps HeartbeatTime; concurrent claimers obtain
	// disjoint sets of jobs. The staleness stamp and the staleness comparison
	// use the same clock domain (store-side, where the store has one).
	// Cross-tenant, like ReapExpired: obtain with a background/tenant-less
	// context, as with ScheduledTaskStore.ScanDue (persistence.go:19-24).
	ClaimStale(ctx context.Context, staleAfter time.Duration, limit int) ([]*SearchJob, error)

	// ClearResults deletes the job's persisted result IDs. Idempotent.
	ClearResults(ctx context.Context, jobID string) error
}
