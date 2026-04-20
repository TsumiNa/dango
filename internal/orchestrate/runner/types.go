package runner

import (
	"context"
	"errors"
	"time"
)

// Executor is the minimal execution contract a Node needs.
type Executor interface {
	Execute(ctx context.Context, parentOutputs map[string]any) (output any, newNodes []*Node, err error)
}

// EventType defines the lifecycle events published by the runner.
type EventType uint8

const (
	EventNodeAdded EventType = iota
	EventNodeStarted
	EventNodeCompleted
	EventNodeFailed
	EventEngineIdle
	EventEngineStopped
)

func (e EventType) String() string {
	switch e {
	case EventNodeAdded:
		return "NodeAdded"
	case EventNodeStarted:
		return "NodeStarted"
	case EventNodeCompleted:
		return "NodeCompleted"
	case EventNodeFailed:
		return "NodeFailed"
	case EventEngineIdle:
		return "EngineIdle"
	case EventEngineStopped:
		return "EngineStopped"
	default:
		return "Unknown"
	}
}

// RunnerEvent represents a notification regarding graph execution states.
type RunnerEvent struct {
	Type   EventType
	NodeID string
	Data   any // Output for Completed, err for Failed
}

// RunnerSnapshot is a queryable freeze-frame of the engine's state.
type RunnerSnapshot struct {
	ActiveCount    int
	CompletedNodes map[string]any
	PendingNodes   map[string]int
	GraphEdges     map[string][]string // parent -> children IDs
	NodesData      map[string]*Node    // Make nodes accessible for snapshot reading
}

// RunnerStatus reports the current lifecycle state of a Runner.
type RunnerStatus string

const (
	RunnerStatusPending  RunnerStatus = "pending"
	RunnerStatusRunning  RunnerStatus = "running"
	RunnerStatusIdle     RunnerStatus = "idle"
	RunnerStatusFailed   RunnerStatus = "failed"
	RunnerStatusCanceled RunnerStatus = "canceled"
)

// RunnerState is the externally visible lifecycle snapshot of a Runner.
type RunnerState struct {
	Status     RunnerStatus `json:"status" yaml:"status"`
	StartedAt  time.Time    `json:"started_at,omitempty" yaml:"started_at,omitempty"`
	UpdatedAt  time.Time    `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
	FinishedAt time.Time    `json:"finished_at,omitempty" yaml:"finished_at,omitempty"`
	Error      string       `json:"error,omitempty" yaml:"error,omitempty"`
}

// Node represents a single unit of work within the Runner's execution graph.
type Node struct {
	Id      string  `json:"id" yaml:"id"`
	Parents []*Node `json:"parents,omitempty" yaml:"parents,omitempty"`
	// Executor contains the execution logic of the node.
	Executor Executor `json:"-" yaml:"-"`

	CreatedAt  time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt  time.Time `json:"updated_at" yaml:"updated_at"`
	FinishedAt time.Time `json:"finished_at" yaml:"finished_at"`
}

type executionResult struct {
	nodeID   string
	output   any
	newNodes []*Node
	err      error
}

// ErrRunnerAlreadyStarted is returned when callers attempt to start or
// configure persistence on a Runner that has already started.
var ErrRunnerAlreadyStarted = errors.New("orchestrate: runner already started")
