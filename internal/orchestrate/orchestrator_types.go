package orchestrate

import (
	"errors"

	"github.com/tsumina/dango/internal/llm/skill"
)

// ErrRunnerNotFound is returned when an Orchestrator runner lookup misses.
var ErrRunnerNotFound = errors.New("orchestrate: runner not found")

// ErrRunnerActive is returned when callers attempt to remove a runner that
// is still live and may continue to accept work.
var ErrRunnerActive = errors.New("orchestrate: runner is still active")

// ErrRunnerStoreNotConfigured is returned when persisted runner records are
// requested without a configured runner store.
var ErrRunnerStoreNotConfigured = errors.New("orchestrate: runner store not configured")

// PlanningFunc analyzes req against the registered skills and returns either a
// coarse plan or a structured reason the task cannot proceed.
type PlanningFunc func(req *Request, skills map[string]*skill.Skill) (*CoarsePlan, *RejectReason, error)

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
	Input    string          `json:"input" yaml:"input"`
	Priority RequestPriority `json:"priority,omitempty" yaml:"priority,omitempty"`
}

// CoarsePlan is the Orchestrator's high-level task graph before execution
// starts.
type CoarsePlan struct {
	Request  string           `json:"request" yaml:"request"`
	RunnerID string           `json:"runner_id,omitempty" yaml:"runner_id,omitempty"`
	Nodes    []CoarsePlanNode `json:"nodes" yaml:"nodes"`
}

// CoarsePlanNode describes one executor-sized unit in a coarse plan.
type CoarsePlanNode struct {
	ID              string   `json:"id" yaml:"id"`
	SkillName       string   `json:"skill_name" yaml:"skill_name"`
	TaskDescription string   `json:"task_description" yaml:"task_description"`
	DependsOn       []string `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
}

// RejectReason explains why a request cannot currently be turned into a plan.
type RejectReason struct {
	Summary       string   `json:"summary" yaml:"summary"`
	Analysis      string   `json:"analysis" yaml:"analysis"`
	MissingSkills []string `json:"missing_skills,omitempty" yaml:"missing_skills,omitempty"`
}
