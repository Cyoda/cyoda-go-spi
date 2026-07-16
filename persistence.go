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
// CurrentState. Returns the cancelled tasks (for audit).
type ReconcileRequest struct {
	TenantID     TenantID
	EntityID     string
	CurrentState string
	Arm          []ScheduledTask // tasks to Upsert (current state's schedules)
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
	// ReconcileForEntity upserts req.Arm and deletes the entity's other-state
	// pending tasks; returns the deleted (cancelled) tasks.
	ReconcileForEntity(ctx context.Context, req ReconcileRequest) (cancelled []ScheduledTask, err error)
}

type EntityStore interface {
	Save(ctx context.Context, entity *Entity) (int64, error)
	// CompareAndSave saves the entity only if the current latest transaction ID matches expectedTxID.
	// Returns ErrConflict if the transaction ID has changed.
	CompareAndSave(ctx context.Context, entity *Entity, expectedTxID string) (int64, error)
	// SaveAll saves multiple entities, returning versions in iteration order.
	// Backends may execute saves concurrently. On error, returns the first
	// error encountered; partially-saved entities within an uncommitted
	// transaction are invisible to readers.
	SaveAll(ctx context.Context, entities iter.Seq[*Entity]) ([]int64, error)
	Get(ctx context.Context, entityID string) (*Entity, error)
	GetAsAt(ctx context.Context, entityID string, asAt time.Time) (*Entity, error)
	GetAll(ctx context.Context, modelRef ModelRef) ([]*Entity, error)
	GetAllAsAt(ctx context.Context, modelRef ModelRef, asAt time.Time) ([]*Entity, error)
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
	GetVersionHistory(ctx context.Context, entityID string) ([]EntityVersion, error)
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
