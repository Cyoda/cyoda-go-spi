package spi

import (
	"context"
	"io"
	"iter"
	"time"
)

type StoreFactory interface {
	EntityStore(ctx context.Context) (EntityStore, error)
	ModelStore(ctx context.Context) (ModelStore, error)
	KeyValueStore(ctx context.Context) (KeyValueStore, error)
	MessageStore(ctx context.Context) (MessageStore, error)
	WorkflowStore(ctx context.Context) (WorkflowStore, error)
	StateMachineAuditStore(ctx context.Context) (StateMachineAuditStore, error)
	AsyncSearchStore(ctx context.Context) (AsyncSearchStore, error)
	// ScheduledTaskStore accesses durable scheduled tasks. Unlike the
	// per-tenant stores, its ScanDue is cross-tenant (obtain with a
	// background/tenant-less context); Upsert/Delete/Reconcile carry the
	// tenant on the task/request. Participates in the entity write's
	// transaction so arm/cancel are atomic with the state change.
	ScheduledTaskStore(ctx context.Context) (ScheduledTaskStore, error)
	TransactionManager(ctx context.Context) (TransactionManager, error)
	Close() error
}

// ReconcileForEntity input: arm the CurrentState's scheduled transitions,
// cancel (delete) any pending task for this entity whose SourceState !=
// CurrentState, and additionally delete the tasks explicitly listed in
// Cancel. Returns the cancelled tasks (for audit); the Cancel-driven
// deletions are reported distinctly from the SourceState-mismatch cancels.
type ReconcileRequest struct {
	TenantID     TenantID
	EntityID     string
	CurrentState string
	Arm          []ScheduledTask // tasks to Upsert (current state's schedules)
	// Cancel lists task IDs to delete for this transaction regardless of
	// SourceState, e.g. born-expired scheduled transitions computed by a
	// ScheduleFunction whose result already lies in the past. Audited
	// separately from the SourceState-mismatch cancels.
	Cancel []string
}

// ScheduledTaskStore persists ScheduledTasks. Arm/Delete/Reconcile MUST
// participate in the caller's transaction (atomic with the entity write).
// ScanDue is a read across all tenants and is called outside any tenant tx.
type ScheduledTaskStore interface {
	Upsert(ctx context.Context, task ScheduledTask) error
	Get(ctx context.Context, id string) (task *ScheduledTask, found bool, err error)
	// ScanDue returns up to limit tasks with ScheduledTime <= nowMs AND
	// (RedispatchAfter is null OR <= nowMs), ordered by ScheduledTime, across tenants.
	ScanDue(ctx context.Context, nowMs int64, limit int) ([]ScheduledTask, error)
	// MarkRedispatch sets RedispatchAfter = redispatchAfterMs (plain write) and bumps AttemptCount.
	MarkRedispatch(ctx context.Context, id string, redispatchAfterMs int64) error
	// Delete removes the task, returning whether a row was actually removed
	// (delete-gated terminal audit relies on this).
	Delete(ctx context.Context, id string) (removed bool, err error)
	// ReconcileForEntity upserts req.Arm, deletes the entity's other-state
	// pending tasks, and additionally deletes the tasks listed in req.Cancel
	// (audited distinctly from the SourceState-mismatch cancels); returns
	// the deleted (cancelled) tasks.
	ReconcileForEntity(ctx context.Context, req ReconcileRequest) (cancelled []ScheduledTask, err error)
}

type EntityStore interface {
	Save(ctx context.Context, entity *Entity) (int64, error)
	// CompareAndSave saves the entity only if expectedTxID matches the
	// entity's current transaction ID as the caller's own transaction sees
	// it: a same-transaction Delete or Save IS the current state, not the
	// pre-transaction one, so comparing against a stale ID — including the
	// transaction's own prior write — conflicts. The comparison is
	// literal, with no synonyms: a missing or deleted entity has the empty
	// transaction ID, so expectedTxID == "" means "expect no entity" and
	// creates one; a non-empty expectedTxID against a missing entity
	// conflicts rather than creating.
	// Returns ErrConflict if the transaction ID has changed.
	CompareAndSave(ctx context.Context, entity *Entity, expectedTxID string) (int64, error)
	// SaveAll saves multiple entities, returning versions in iteration order.
	// Backends may execute saves concurrently. On error, returns the first
	// error encountered; partially-saved entities within an uncommitted
	// transaction are invisible to readers.
	SaveAll(ctx context.Context, entities iter.Seq[*Entity]) ([]int64, error)
	Get(ctx context.Context, entityID string) (*Entity, error)
	// GetAsAt returns entityID as of asAt. Like every point-in-time read in
	// this SPI it is COMMITTED-ONLY: it ignores any ambient transaction and
	// never surfaces that transaction's own uncommitted writes — an entity
	// the transaction created is ErrNotFound, and one it updated comes back
	// at its committed payload. A backend whose ordinary reads join the
	// caller's transaction must route this read off it; bounding the query on
	// a timestamp is not sufficient, because a transaction-stable clock makes
	// the transaction's own writes fall inside every window it can compute.
	GetAsAt(ctx context.Context, entityID string, asAt time.Time) (*Entity, error)
	Delete(ctx context.Context, entityID string) error
	DeleteAll(ctx context.Context, modelRef ModelRef) error
	Exists(ctx context.Context, entityID string) (bool, error)
	Count(ctx context.Context, modelRef ModelRef) (int64, error)
	// CountByState returns the count of non-deleted entities grouped by state
	// for the given model. If states is non-nil, only the listed states are
	// included in the result. If states is nil, all states are returned.
	// An empty (non-nil) states slice returns an empty map without querying
	// the storage layer.
	//
	// Unknown model: returns an empty map with no error, matching Count's
	// behavior (no model-registry check at this layer).
	//
	// Implementations MUST push the state filter down to the storage layer
	// when feasible. Callers may invoke this from inside a transaction; the
	// returned counts MUST reflect the transactional view (uncommitted writes
	// from the current tx are visible, writes from other in-flight txs are not),
	// matching the semantics of Count.
	CountByState(ctx context.Context, modelRef ModelRef, states []string) (map[string]int64, error)

	// GetPage returns a page of modelRef's entities in the engine's
	// canonical per-engine entity-ID order (see OrderSpec's doc comment —
	// this is the same order Search/Iterate use for an empty/id-only
	// OrderBy, not guaranteed identical across backends).
	//
	// limit >= 1 && offset >= 0 is REQUIRED; either violation is a contract
	// violation and the implementation MUST return an error rather than
	// substituting a default. Implementations fail fast on any row-level
	// error rather than returning a partial page.
	//
	// asAt == nil reads the live, in-transaction overlay: with an ambient
	// transaction, the committed page is merged with the transaction's own
	// write-set, and — unconditionally, unlike Searcher's opt-in
	// TrackingRead — every entity on the returned page is recorded in the
	// transaction's read-set. asAt != nil ignores any ambient transaction
	// and reads committed-only state as of that instant.
	GetPage(ctx context.Context, modelRef ModelRef, limit, offset int, asAt *time.Time) ([]*Entity, error)

	// GetVersionByTransaction returns the earliest version of entityID
	// written by transaction txID. A transaction that saved the same
	// entity more than once before committing (e.g. two Save calls inside
	// one commit) may produce more than one matching version; the
	// earliest (lowest Version) is returned.
	//
	// Versions with no entity payload — DELETED tombstones — never match,
	// even when txID is the deleting transaction's own ID: this method
	// surfaces entity content, and a tombstone has none. Use
	// GetVersionMetadata to read a tombstone's metadata instead.
	//
	// An empty txID never matches a stored-empty TransactionID
	// (non-transactional writes carry one); it always returns ErrNotFound.
	GetVersionByTransaction(ctx context.Context, entityID, txID string) (*EntityVersion, error)

	// GetVersionMetadata returns entityID's version metadata — no entity
	// payload, just the audit trail — newest first, ties broken by
	// Version DESC. opts.From/opts.Until bound the window inclusively; a
	// nil side is unbounded. opts.Limit caps the returned row count; 0
	// means all, bounded only by this one entity's own version history —
	// a deliberate divergence from GetPage's limit>=1 requirement, since
	// a single entity's history can never be an unbounded model-wide scan.
	//
	// Returns ErrNotFound ONLY when entityID has no version history at all;
	// an existing entity whose versions all fall outside opts.From/opts.Until
	// yields an empty slice and a nil error, never ErrNotFound.
	//
	// Deleted is true only on the DELETED tombstone row, and Version is
	// populated on every returned row, including the tombstone.
	GetVersionMetadata(ctx context.Context, entityID string, opts VersionMetadataOptions) ([]EntityVersionMeta, error)

	// Search is the bounded-or-fail predicate read: SearchOptions.Limit >= 1
	// is REQUIRED and caps the matched set; more matches than Limit MUST be
	// ErrSearchResultLimitExceeded, never a truncated prefix; exactly at the
	// limit succeeds; Limit <= 0 is a contract violation and MUST error.
	// Search honours an active transaction (read-your-own-writes) unless
	// PointInTime is set, in which case it is committed-only. Returned
	// entities enter the read-set only when SearchOptions.TrackingRead is
	// set. See SearchOptions.
	Search(ctx context.Context, filter Filter, opts SearchOptions) ([]*Entity, error)

	// Iterate is the streamed predicate read: entities matching filter, one
	// at a time, in bounded memory. A zero-value Filter yields every entity
	// of the model. Pushable parts of the filter go to storage; the residual
	// is applied inside Next(). With an ambient transaction the merged
	// (committed ∪ write-set) view is snapshotted at the call; mutating the
	// transaction while an iterator is open is forbidden. Implementations
	// MUST NOT hold a write-blocking lock for the iterator's lifetime, MUST
	// observe ctx cancellation, surface the first error stickily via Err(),
	// and make Close() idempotent. See IterateOptions and Iterator.
	//
	// Every engine path that reads more than one entity — direct search on
	// a store, async search, delete-all, conditional delete, grouped stats —
	// consumes Search or Iterate. There is no whole-model read on this
	// interface and no in-process fallback in the engine.
	Iterate(ctx context.Context, model ModelRef, filter Filter, opts IterateOptions) (Iterator, error)
}

// SchemaDelta is an opaque, plugin-agnostic serialization of an
// additive schema change. Bytes are produced by the consuming
// application's schema diff logic (e.g. cyoda-go's
// internal/domain/model/schema) and replayed by an injected apply
// function in the plugin. Plugins persist bytes verbatim; they MUST
// NOT interpret them.
type SchemaDelta []byte

type ModelStore interface {
	Save(ctx context.Context, desc *ModelDescriptor) error
	Get(ctx context.Context, modelRef ModelRef) (*ModelDescriptor, error)
	GetAll(ctx context.Context) ([]ModelRef, error)
	Delete(ctx context.Context, modelRef ModelRef) error
	Lock(ctx context.Context, modelRef ModelRef) error
	Unlock(ctx context.Context, modelRef ModelRef) error
	IsLocked(ctx context.Context, modelRef ModelRef) (bool, error)
	SetChangeLevel(ctx context.Context, modelRef ModelRef, level ChangeLevel) error
	// ExtendSchema appends a schema delta for the model at ref. The
	// delta is an opaque, plugin-agnostic blob that the plugin stores
	// verbatim in its extension log; folding the log into the current
	// schema is done on read via a plugin-injected ApplyFunc.
	//
	// Contract:
	//   - Success (nil return) means the extension is durably committed
	//     and visible to subsequent reads on this node.
	//   - A non-nil error means no persisted effect — no log entry,
	//     no savepoint, no partial state.
	//   - Plugins with a native conflict surface (sqlite SQLITE_BUSY,
	//     cassandra LWT applied:false) retry transparently up to a
	//     configurable budget. On exhaustion without ctx cancellation,
	//     return ErrRetryExhausted.
	//   - Context cancellation between retry attempts returns ctx.Err()
	//     (wrapped with attempt count), not ErrRetryExhausted. Mid-attempt
	//     cancellation follows backend-native behavior.
	//   - Plugins without a conflict surface (memory, postgres) commit
	//     immediately or fail with the backend's native error.
	//
	// Empty or nil deltas are a no-op and return nil.
	ExtendSchema(ctx context.Context, ref ModelRef, delta SchemaDelta) error
}

type KeyValueStore interface {
	Put(ctx context.Context, namespace string, key string, value []byte) error
	Get(ctx context.Context, namespace string, key string) ([]byte, error)
	Delete(ctx context.Context, namespace string, key string) error
	List(ctx context.Context, namespace string) (map[string][]byte, error)
}

type MessageStore interface {
	Save(ctx context.Context, id string, header MessageHeader, metaData MessageMetaData, payload io.Reader) error
	Get(ctx context.Context, id string) (MessageHeader, MessageMetaData, io.ReadCloser, error)
	Delete(ctx context.Context, id string) error
	DeleteBatch(ctx context.Context, ids []string) error
}

type WorkflowStore interface {
	Save(ctx context.Context, modelRef ModelRef, workflows []WorkflowDefinition) error
	Get(ctx context.Context, modelRef ModelRef) ([]WorkflowDefinition, error)
	Delete(ctx context.Context, modelRef ModelRef) error
}

type StateMachineAuditStore interface {
	Record(ctx context.Context, entityID string, event StateMachineEvent) error
	GetEvents(ctx context.Context, entityID string) ([]StateMachineEvent, error)
	GetEventsByTransaction(ctx context.Context, entityID string, transactionID string) ([]StateMachineEvent, error)
}
