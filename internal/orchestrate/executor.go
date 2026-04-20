package orchestrate

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/tsumina/dango/internal/llm/skill"
	runnerpkg "github.com/tsumina/dango/internal/orchestrate/runner"
)

// Status reports the lifecycle state of an [Executor].
type Status int

const (
	StatusPending Status = iota
	StatusRunning
	StatusDone
	StatusFailed
)

// ExecutionPlanner carries the working description, reasoning, and proposed
// solution for a task that an [Executor] is about to run. It is mutated in
// place by planning steps such as [Executor.PolishPlan].
type ExecutionPlanner struct {
	id              string
	TaskDescription string `json:"task_description" yaml:"description"`
	Reason          string `json:"reason" yaml:"reason"`
	Solution        string `json:"solution" yaml:"solution"`
	Version         uint32 `json:"version" yaml:"version"`
}

// ExecutionResult is the structured outcome an [Executor] produces after a
// task finishes, including any data it wants to share with downstream nodes.
type ExecutionResult struct {
	Success    bool         `json:"success" yaml:"success"`
	Message    string       `json:"message" yaml:"message"`
	Handoff    bool         `json:"handoff" yaml:"handoff"`
	SharedData []SharedData `json:"shared_data,omitempty" yaml:"shared_data,omitempty"`
}

// SharedData describes a single artifact an [Executor] hands off to other
// tasks via [ExecutionResult.SharedData].
type SharedData struct {
	FilePath    string `json:"file_path" yaml:"file_path"`
	Description string `json:"description" yaml:"description"`
}

// Executor runs a single task on top of a loaded [skill.Skill].
//
// An Executor is bound to one Skill at construction time and uses the
// Skill's directory, prompt, and bound LLM client to plan and run the
// task. The zero value is not usable; construct instances with
// [NewExecutor].
type Executor struct {
	logger  *slog.Logger
	skill   *skill.Skill
	planner *ExecutionPlanner

	// Result holds the structured outcome of the most recent execution.
	Result *ExecutionResult
	// Status reports the executor's current lifecycle state.
	Status Status

	// RunE optionally overrides the default execution path. It is the
	// hook the runner tests use to inject behavior into an Executor
	// without depending on a real skill or LLM client.
	RunE func(ctx context.Context, parentOutputs map[string]any) (output any, newNodes []*runnerpkg.Node, err error)
}

// NewExecutor constructs an [Executor] bound to sk and planner.
//
// logger receives lifecycle log messages and may be nil to silence them.
// sk and planner must be non-nil; sk supplies the workspace directory,
// metadata, instruction prompt, and LLM client used during execution.
func NewExecutor(logger *slog.Logger, sk *skill.Skill, planner *ExecutionPlanner) (*Executor, error) {
	if sk == nil {
		return nil, fmt.Errorf("orchestrate: executor requires a non-nil skill")
	}
	if planner == nil {
		return nil, fmt.Errorf("orchestrate: executor requires a non-nil planner")
	}
	if logger != nil {
		logger.Info("Creating a new Executor")
	}
	return &Executor{
		logger:  logger,
		skill:   sk,
		planner: planner,
	}, nil
}

// Skill returns the [skill.Skill] this executor was bound to.
func (e *Executor) Skill() *skill.Skill { return e.skill }

// Planner returns the [ExecutionPlanner] this executor mutates during
// planning.
func (e *Executor) Planner() *ExecutionPlanner { return e.planner }

// PolishPlan refines the planner's reasoning and solution based on the
// current task description. It bumps [ExecutionPlanner.Version] on success.
func (e *Executor) PolishPlan() error {
	e.logf("Planning tasks...")

	if err := e.planTask(); err != nil {
		e.logf("Error planning tasks: %v", err)
		return err
	}
	return nil
}

func (e *Executor) planTask() error {
	e.logf("Planning a task...")

	e.planner.Reason = "The task requires processing data and generating a report."
	e.planner.Solution = "Use a data processing library to analyze the data and generate the report."
	e.planner.Version++
	return nil
}

// Execute runs the task. When [Executor.RunE] is set it is invoked
// directly; otherwise Execute is currently a no-op placeholder until the
// real skill-driven execution path is implemented.
func (e *Executor) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*runnerpkg.Node, error) {
	e.logf("Executing tasks...")

	if e.RunE != nil {
		return e.RunE(ctx, parentOutputs)
	}
	return nil, nil, nil
}

// Polish satisfies the runner's polish contract. The default implementation
// refreshes the planner via [Executor.PolishPlan] and returns a snapshot of
// the planner as the fragment.
func (e *Executor) Polish(ctx context.Context) (any, error) {
	if err := e.PolishPlan(); err != nil {
		return nil, err
	}
	return *e.planner, nil
}

// Report satisfies the runner's report contract. The default implementation
// passes the execution output through unchanged as the summary.
func (e *Executor) Report(ctx context.Context, output any) (any, error) {
	return output, nil
}

func (e *Executor) logf(format string, args ...any) {
	if e.logger == nil {
		return
	}
	e.logger.Debug(fmt.Sprintf(format, args...))
}
