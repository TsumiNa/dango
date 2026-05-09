package stream

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Status values shared by producers that emit high-level progress.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Event type names shared by the first stream-system consumers.
const (
	EventStatusStarted   = "status.started"
	EventStatusProgress  = "status.progress"
	EventStatusCompleted = "status.completed"
	EventStatusFailed    = "status.failed"

	EventLLMReasoningDelta    = "llm.reasoning.delta"
	EventLLMOutputDelta       = "llm.output.delta"
	EventLLMToolCallStarted   = "llm.tool_call.started"
	EventLLMToolCallDelta     = "llm.tool_call.delta"
	EventLLMToolCallCompleted = "llm.tool_call.completed"
	EventLLMToolResultDelta   = "llm.tool_result.delta"

	EventToolExecutionStarted   = "tool.execution.started"
	EventToolExecutionCompleted = "tool.execution.completed"
	EventToolExecutionFailed    = "tool.execution.failed"

	EventRunnerPhaseChanged  = "runner.phase.changed"
	EventRunnerNodeAdded     = "runner.node.added"
	EventRunnerNodeStarted   = "runner.node.started"
	EventRunnerNodeCompleted = "runner.node.completed"
	EventRunnerNodeFailed    = "runner.node.failed"

	EventExecutorPolishStarted    = "executor.polish.started"
	EventExecutorPolishCompleted  = "executor.polish.completed"
	EventExecutorPolishFailed     = "executor.polish.failed"
	EventExecutorExecuteStarted   = "executor.execute.started"
	EventExecutorExecuteCompleted = "executor.execute.completed"
	EventExecutorExecuteFailed    = "executor.execute.failed"
	EventExecutorReportStarted    = "executor.report.started"
	EventExecutorReportCompleted  = "executor.report.completed"
	EventExecutorReportFailed     = "executor.report.failed"

	EventExchangePublished = "exchange.published"
	EventHandoffEmitted    = "handoff.emitted"
	EventHandoffDelivered  = "handoff.delivered"
	EventMemoSnapshot      = "memo.snapshot"

	EventSkillMemoDelta  = "skill.memo.delta"
	EventArtifactCreated = "artifact.created"
)

var (
	// ErrClosed is returned when callers use a stream after it has closed.
	ErrClosed = errors.New("stream: closed")

	// ErrInvalidEvent is wrapped when an event cannot be emitted as valid JSON.
	ErrInvalidEvent = errors.New("stream: invalid event")

	// ErrInvalidMerge is returned when streams cannot be connected.
	ErrInvalidMerge = errors.New("stream: invalid merge")
)

// Event is one JSON-unmarshalable chunk emitted by a producer.
//
// EventType, From, SequenceNumber, Status, and Delta are required on every
// emitted event. SequenceNumber is assigned by Stream.Emit so producers do not
// need to coordinate numbering.
//
// LogicalTime is a monotonically increasing timestamp within a stream that
// provides stable ordering across bundles, replay, and debugging. It is assigned
// by Stream.Emit before the per-stream sequence number.
type Event struct {
	EventType      string          `json:"event_type"`
	From           Source          `json:"from"`
	SequenceNumber uint64          `json:"sequence_number"`
	Status         string          `json:"status"`
	Delta          json.RawMessage `json:"delta"`
	LogicalTime    uint64          `json:"logical_time,omitempty"`
	Timestamp      time.Time       `json:"timestamp,omitempty"`
	Scope          Scope           `json:"scope,omitempty"`
	Metadata       map[string]any  `json:"metadata,omitempty"`
}

// Source identifies the component that produced an event.
type Source struct {
	Layer    string `json:"layer"`
	ID       string `json:"id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
}

// Scope carries correlation IDs shared across events in one logical run.
type Scope struct {
	RequestID string `json:"request_id,omitempty"`
	RunnerID  string `json:"runner_id,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

func (ev Event) prepare(scope Scope, sequence uint64, logicalTime uint64, now func() time.Time) (Event, error) {
	if ev.EventType == "" {
		return Event{}, fmt.Errorf("%w: missing event_type", ErrInvalidEvent)
	}
	if ev.From.Layer == "" {
		return Event{}, fmt.Errorf("%w: missing from.layer", ErrInvalidEvent)
	}
	if ev.Status == "" {
		return Event{}, fmt.Errorf("%w: missing status", ErrInvalidEvent)
	}
	if ev.Delta == nil {
		ev.Delta = json.RawMessage("null")
	}
	if !json.Valid(ev.Delta) {
		return Event{}, fmt.Errorf("%w: invalid delta JSON", ErrInvalidEvent)
	}
	ev.SequenceNumber = sequence
	ev.LogicalTime = logicalTime
	if ev.Timestamp.IsZero() {
		ev.Timestamp = now().UTC()
	}
	ev.Scope = mergeScope(scope, ev.Scope)
	if _, err := json.Marshal(ev); err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	return ev, nil
}

func mergeScope(base Scope, override Scope) Scope {
	out := override
	if out.RequestID == "" {
		out.RequestID = base.RequestID
	}
	if out.RunnerID == "" {
		out.RunnerID = base.RunnerID
	}
	if out.NodeID == "" {
		out.NodeID = base.NodeID
	}
	if out.SessionID == "" {
		out.SessionID = base.SessionID
	}
	return out
}
