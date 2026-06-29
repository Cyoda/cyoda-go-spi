package spi

import (
	"encoding/json"
	"fmt"
	"time"
)

type ModelRef struct {
	EntityName   string
	ModelVersion string
}

func (r ModelRef) String() string {
	return r.EntityName + "." + r.ModelVersion
}

type EntityMeta struct {
	ID                      string
	TenantID                TenantID
	ModelRef                ModelRef
	State                   string
	Version                 int64
	CreationDate            time.Time
	LastModifiedDate        time.Time
	TransactionID           string
	ChangeType              string // "CREATED", "UPDATED", "DELETED"
	ChangeUser              string // user ID who performed the change
	TransitionForLatestSave string
}

type Entity struct {
	Meta EntityMeta
	Data []byte
}

// EntityVersion represents a single version entry in an entity's change history.
type EntityVersion struct {
	Entity     *Entity
	ChangeType string
	User       string
	Timestamp  time.Time
	Version    int64
	Deleted    bool
}

// ModelState represents the lifecycle state of an entity model.
type ModelState string

const (
	ModelLocked   ModelState = "LOCKED"
	ModelUnlocked ModelState = "UNLOCKED"
)

// ChangeLevel controls which structural changes are permitted during data ingestion.
type ChangeLevel string

const (
	ChangeLevelArrayLength   ChangeLevel = "ARRAY_LENGTH"
	ChangeLevelArrayElements ChangeLevel = "ARRAY_ELEMENTS"
	ChangeLevelType          ChangeLevel = "TYPE"
	ChangeLevelStructural    ChangeLevel = "STRUCTURAL"
)

// validChangeLevels is the set of recognized ChangeLevel values.
var validChangeLevels = map[ChangeLevel]bool{
	ChangeLevelArrayLength:   true,
	ChangeLevelArrayElements: true,
	ChangeLevelType:          true,
	ChangeLevelStructural:    true,
}

// ValidateChangeLevel returns an error if the given string is not a known ChangeLevel.
func ValidateChangeLevel(s string) (ChangeLevel, error) {
	cl := ChangeLevel(s)
	if !validChangeLevels[cl] {
		return "", fmt.Errorf("BAD_REQUEST: invalid change level %q; valid values: ARRAY_LENGTH, ARRAY_ELEMENTS, TYPE, STRUCTURAL", s)
	}
	return cl, nil
}

// ModelDescriptor holds the full metadata and schema for an entity model.
type ModelDescriptor struct {
	Ref         ModelRef
	State       ModelState
	ChangeLevel ChangeLevel
	UpdateDate  time.Time
	Schema      []byte
	// UniqueKeys are the model's composite unique-key definitions. Additive;
	// persisted inside the descriptor by each model store. Empty = none.
	UniqueKeys []UniqueKey
}

// MessageHeader holds the fixed AMQP-aligned headers for an edge message.
type MessageHeader struct {
	Subject         string
	ContentType     string
	ContentLength   int64
	ContentEncoding string
	MessageID       string // custom message ID from X-Message-ID header
	UserID          string
	Recipient       string
	ReplyTo         string
	CorrelationID   string
}

// MessageMetaData holds arbitrary key-value metadata for an edge message.
// Values preserve their original JSON types (string, number, bool, etc.).
type MessageMetaData struct {
	Values        map[string]any
	IndexedValues map[string]any
}

// --- Workflow types ---

// WorkflowDefinition represents a complete workflow configuration.
type WorkflowDefinition struct {
	Version      string                     `json:"version"`
	Name         string                     `json:"name"`
	Description  string                     `json:"desc,omitempty"`
	InitialState string                     `json:"initialState"`
	Active       bool                       `json:"active"`
	Criterion    json.RawMessage            `json:"criterion,omitempty"`
	States       map[string]StateDefinition `json:"states"`
	// Annotations is arbitrary client-owned metadata, stored and
	// round-tripped verbatim and never interpreted by the engine.
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// StateDefinition represents a state with its transitions.
type StateDefinition struct {
	Transitions []TransitionDefinition `json:"transitions,omitempty"`
	// Annotations is arbitrary client-owned metadata, stored and
	// round-tripped verbatim and never interpreted by the engine.
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// TransitionDefinition represents a single transition from a state.
type TransitionDefinition struct {
	Name       string                `json:"name"`
	Next       string                `json:"next"`
	Manual     bool                  `json:"manual"`
	Disabled   bool                  `json:"disabled,omitempty"`
	Criterion  json.RawMessage       `json:"criterion,omitempty"`
	Processors []ProcessorDefinition `json:"processors,omitempty"`
	Schedule   *TransitionSchedule   `json:"schedule,omitempty"`
	// Annotations is arbitrary client-owned metadata, stored and
	// round-tripped verbatim and never interpreted by the engine.
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// ProcessorDefinition represents a processor attached to a transition.
type ProcessorDefinition struct {
	// Type is the execution-location axis. Recognised values are defined
	// by the cyoda-go engine package; canonical values are "externalized"
	// (dispatched via gRPC to a calculation node selected by
	// Config.CalculationNodesTags) and "internalized" (reserved for an
	// in-process execution location, currently rejected at engine
	// dispatch as not yet implemented). Empty is treated as "externalized".
	// Any value other than "internalized" falls through to the
	// ExecutionMode dispatch path; import-time validation does not
	// constrain this field.
	Type          string          `json:"type"`
	Name          string          `json:"name"`
	ExecutionMode string          `json:"executionMode,omitempty"`
	Config        ProcessorConfig `json:"config,omitempty"`
}

// ProcessorConfig holds configuration for a processor.
type ProcessorConfig struct {
	AttachEntity         bool   `json:"attachEntity,omitempty"`
	CalculationNodesTags string `json:"calculationNodesTags,omitempty"`
	ResponseTimeoutMs    int64  `json:"responseTimeoutMs,omitempty"`
	RetryPolicy          string `json:"retryPolicy,omitempty"`
	Context              string `json:"context,omitempty"`
	// StartNewTxOnDispatch, when true and ExecutionMode is COMMIT_BEFORE_DISPATCH,
	// causes the cascade engine to open a fresh transaction before dispatching
	// the processor (so the processor may perform transactional work via that
	// tx's token). When false (default) the processor runs with no transaction
	// context and the connection is released entirely during dispatch.
	// Ignored for any other ExecutionMode.
	StartNewTxOnDispatch *bool `json:"startNewTxOnDispatch,omitempty"`

	// AsyncResult, when true, requests that the cascade engine suspend
	// the transaction at processor dispatch and resume only when the
	// processor's result eventually arrives via the async-result
	// delivery slot. The runtime that implements this — durable
	// suspend state, work-stealing recovery, distributed timer
	// coordination — is gated on storage-engine primitives not
	// available in every backend. Consuming engines that do not
	// implement async-result semantics MUST reject this field at
	// import (or the equivalent configuration-boundary) rather than
	// silently degrade to synchronous dispatch.
	//
	// Pointer so that nil (absent) and &false (explicit no-async) are
	// distinguishable on the wire and round-trip byte-equivalent.
	AsyncResult *bool `json:"asyncResult,omitempty"`

	// CrossoverToAsyncMs is the timer, in milliseconds, after which
	// the engine crosses over from sync-wait to async-result delivery
	// for an AsyncResult=true processor. Effective only when
	// AsyncResult is true. Consuming engines that do not implement
	// async-result semantics MUST reject any non-nil value at import.
	CrossoverToAsyncMs *int64 `json:"crossoverToAsyncMs,omitempty"`
}

// TransitionSchedule configures automatic firing of a future state
// transition. Presence of this struct on a TransitionDefinition marks
// the transition as scheduled.
//
// Semantics. The scheduled execution time of the transition is
// scheduledTime = stateEntryTime + DelayMs. When the scheduler picks
// the task up at executionTime, it computes
// lateness = executionTime - scheduledTime.
//   - If TimeoutMs is nil, the task is always attempted (no timeout).
//   - If TimeoutMs is non-nil and lateness > *TimeoutMs, the task is
//     dropped and the transition is NOT attempted.
//   - If TimeoutMs is non-nil and lateness <= *TimeoutMs (including
//     *TimeoutMs == 0 when lateness is 0), the transition fires.
//
// TimeoutMs gives operators control over how the system handles
// backlog and intermittent-offline conditions. Short positive values
// prefer freshness — stale tasks are discarded rather than fired
// against a possibly-changed entity. Nil prefers eventual execution.
//
// Scheduled transitions are mutually exclusive with Manual=true.
//
// Scheduled transitions are a special case of a generic ScheduledTask
// abstraction. The lateness-tolerance concept (TimeoutMs) applies
// uniformly across all ScheduledTask variants. The generic
// abstraction and the runtime that implements it ship in a later
// release; until then, consuming engines silently skip scheduled
// transitions during automated cascade selection and reject explicit
// fires by name with a transition-not-found error.
type TransitionSchedule struct {
	// DelayMs is the delay between source-state entry and the
	// scheduled execution time, in milliseconds. Must be > 0.
	DelayMs int64 `json:"delayMs"`

	// TimeoutMs is the late-tolerance window past the scheduled
	// execution time, in milliseconds. Nil means no timeout — the
	// task fires whenever the scheduler eventually picks it up.
	// Non-nil zero is the strictest setting — drop on any lateness.
	// Non-nil positive N drops the task if it picks up more than N
	// milliseconds after scheduledTime. Independent of DelayMs; the
	// two measure different quantities.
	TimeoutMs *int64 `json:"timeoutMs,omitempty"`
}

// --- State machine event types ---

// StateMachineEventType represents the type of state machine event.
type StateMachineEventType string

const (
	SMEventStarted                    StateMachineEventType = "STATE_MACHINE_START"
	SMEventFinished                   StateMachineEventType = "STATE_MACHINE_FINISH"
	SMEventCancelled                  StateMachineEventType = "CANCEL"
	SMEventForcedSuccess              StateMachineEventType = "FORCE_SUCCESS"
	SMEventWorkflowFound              StateMachineEventType = "WORKFLOW_FOUND"
	SMEventWorkflowNotFound           StateMachineEventType = "WORKFLOW_NOT_FOUND"
	SMEventWorkflowSkipped            StateMachineEventType = "WORKFLOW_SKIP"
	SMEventTransitionMade             StateMachineEventType = "TRANSITION_MAKE"
	SMEventTransitionNotFound         StateMachineEventType = "TRANSITION_NOT_FOUND"
	SMEventTransitionCriterionNoMatch StateMachineEventType = "TRANSITION_NOT_MATCH_CRITERION"
	SMEventProcessCriterionNoMatch    StateMachineEventType = "PROCESS_NOT_MATCH_CRITERION"
	SMEventProcessingPaused           StateMachineEventType = "PAUSE_FOR_PROCESSING"
	SMEventStateProcessResult         StateMachineEventType = "STATE_PROCESS_RESULT"
)

// StateMachineEvent represents a single event in a state machine execution.
type StateMachineEvent struct {
	EventType     StateMachineEventType `json:"eventType"`
	EntityID      string                `json:"entityId"`
	TimeUUID      string                `json:"timeUuid"`
	State         string                `json:"state,omitempty"`
	TransactionID string                `json:"transactionId,omitempty"`
	Details       string                `json:"details"`
	Data          map[string]any        `json:"data,omitempty"`
	Timestamp     time.Time             `json:"timestamp"`
}

// ExecutionResult holds the outcome of a workflow engine execution.
type ExecutionResult struct {
	State      string
	Success    bool
	StopReason string
	Error      error
}
