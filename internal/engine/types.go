package engine

import (
	"errors"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
)

// ErrRunnerNotFound is returned when an Orchestrator runner lookup misses.
var ErrRunnerNotFound = errors.New("orchestrate: runner not found")

// ErrRunnerActive is returned when callers attempt to remove a runner that
// is still live and may continue to accept work.
var ErrRunnerActive = errors.New("orchestrate: runner is still active")

// ErrRunnerStoreNotConfigured is returned when persisted runner records are
// requested without a configured runner store.
var ErrRunnerStoreNotConfigured = errors.New("orchestrate: runner store not configured")

// ErrRunnerPlanNotAwaitingReview is returned when callers try to accept or
// reject a plan while the runner is not waiting for review.
var ErrRunnerPlanNotAwaitingReview = errors.New("orchestrate: runner plan is not awaiting review")

// ErrRunnerPlanNotAwaitingReplan is returned when callers try to provide a
// replacement plan while the runner is not waiting for replan.
var ErrRunnerPlanNotAwaitingReplan = errors.New("orchestrate: runner plan is not awaiting replan")

// ErrRunnerNotExecuting is returned when callers try to complete a runner
// that is not currently executing.
var ErrRunnerNotExecuting = errors.New("orchestrate: runner is not executing")

// ErrRunnerExecutionSlotsFull is returned when a reviewed runner is ready to
// execute but no execution slot is currently available.
var ErrRunnerExecutionSlotsFull = errors.New("orchestrate: no execution slots available")

// RequestRejectedError reports a planner rejection for a request that could
// not be converted into a runner.
type RequestRejectedError struct {
	Reason *RejectReason
}

func (e *RequestRejectedError) Error() string {
	if e == nil || e.Reason == nil {
		return "orchestrate: request rejected"
	}
	if e.Reason.Summary != "" {
		return "orchestrate: request rejected: " + e.Reason.Summary
	}
	return "orchestrate: request rejected"
}

// CoarsePlan is the orchestrator's high-level task graph before execution
// starts. It is defined in the runner package and re-exported here so
// orchestrator callers can refer to planning results without importing the
// runner package directly.
type CoarsePlan = runnerpkg.CoarsePlan

// CoarsePlanNode describes one executor-sized unit in a [CoarsePlan].
type CoarsePlanNode = runnerpkg.CoarsePlanNode

// RequestPriority orders queued StartRequest submissions.
//
// Valid priorities are the integers 0 through 4 inclusive. The zero value is
// the default priority, and larger values run first when the Orchestrator is
// throttling concurrent runner execution.
type RequestPriority int

const (
	RequestPriorityDefault RequestPriority = 0
	RequestPriorityHighest RequestPriority = 4
)

func (p RequestPriority) valid() bool {
	return p >= RequestPriorityDefault && p <= RequestPriorityHighest
}

// Request is the external task description the Orchestrator receives from the
// caller.
type Request struct {
	Input        string          `json:"input" yaml:"input"`
	Priority     RequestPriority `json:"priority,omitempty" yaml:"priority,omitempty"`
	ArtifactsDir string          `json:"artifacts_dir,omitempty" yaml:"artifacts_dir,omitempty"`
}

// RejectReason explains why a request cannot currently be turned into a plan.
type RejectReason struct {
	Summary       string   `json:"summary" yaml:"summary"`
	Analysis      string   `json:"analysis" yaml:"analysis"`
	MissingSkills []string `json:"missing_skills,omitempty" yaml:"missing_skills,omitempty"`
}

// PlanReview is the planner-owned review decision for a polished plan.
type PlanReview = runnerpkg.PlanReview
