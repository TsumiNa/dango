package engine

import (
	"context"
	"fmt"
	"log/slog"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
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
	SourceInput     string `json:"source_input,omitempty" yaml:"source_input,omitempty"`
	ArtifactsDir    string `json:"artifacts_dir,omitempty" yaml:"artifacts_dir,omitempty"`
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

// Executor runs a single task on top of a loaded [llm.Skill].
//
// An Executor is the engine-owned proxy container for one Skill runtime. It is
// bound to one Skill at construction time, adds node/executor context to the
// skill's runtime stream configuration, and uses the Skill's directory, prompt,
// tools, and bound LLM client to plan and run the task. The zero value is not
// usable; construct instances with [NewExecutor].
type Executor struct {
	logger     *slog.Logger
	skill      *llm.Skill
	planner    *ExecutionPlanner
	bindClient *llm.Client
	bindConfig llm.ConversationConfig
	runtime    *llm.Skill
	// accessibleDirs is the resource directory set most recently passed by the
	// runner for this executor's runtime skill.
	accessibleDirs []string

	// Result holds the structured outcome of the most recent execution.
	Result *ExecutionResult
	// Status reports the executor's current lifecycle state.
	Status Status

	// RunE optionally overrides the default execution path. It is the
	// hook the runner tests use to inject behavior into an Executor
	// without depending on a real skill or LLM client.
	RunE func(ctx context.Context, parentOutputs map[string]any) (output any, newNodes []*runnerpkg.Node, err error)
}

// ExecutorOption adjusts a constructed [Executor] before it is returned.
type ExecutorOption func(*Executor)

// WithExecutorLogger installs logger as the Executor's lifecycle logger.
//
// The Executor keeps a reference to logger. slog.Logger values are safe for
// concurrent use; callers that wrap a handler with additional mutable state are
// responsible for that handler's synchronization.
func WithExecutorLogger(logger *slog.Logger) ExecutorOption {
	return func(e *Executor) {
		e.logger = logger
	}
}

// WithExecutorClient installs client as the LLM client forwarded to
// [llm.Skill.Bind].
//
// The Executor keeps a reference to client and uses it when a runner binds the
// skill. [llm.Client] is safe for concurrent request use, but callers must not
// mutate the shared client or its raw SDK client while runner work is in flight.
func WithExecutorClient(client *llm.Client) ExecutorOption {
	return func(e *Executor) {
		e.bindClient = client
	}
}

// NewExecutor constructs an [Executor] bound to sk and planner.
//
// sk and planner must be non-nil. cfg is later forwarded to [llm.Skill.Bind]
// by runner-owned execution setup.
func NewExecutor(sk *llm.Skill, planner *ExecutionPlanner, cfg llm.ConversationConfig, opts ...ExecutorOption) (*Executor, error) {
	if sk == nil {
		return nil, fmt.Errorf("orchestrate: executor requires a non-nil skill")
	}
	if planner == nil {
		return nil, fmt.Errorf("orchestrate: executor requires a non-nil planner")
	}
	e := &Executor{
		skill:      sk,
		planner:    planner,
		bindConfig: cloneConversationConfig(cfg),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	if e.logger != nil {
		e.logger.Info("Creating a new Executor")
	}
	return e, nil
}

// Skill returns the [llm.Skill] this executor was bound to.
func (e *Executor) Skill() *llm.Skill { return e.skill }

// Planner returns the [ExecutionPlanner] this executor mutates during
// planning.
func (e *Executor) Planner() *ExecutionPlanner { return e.planner }

// LLMClient returns the effective client this executor will use for skill-run
// stages.
func (e *Executor) LLMClient() *llm.Client {
	if e.runtime != nil {
		return e.runtime.Client()
	}
	return e.bindClient
}

// EventStream returns the bound runtime skill's progress stream. The stream is
// created by the skill binding; the executor exposes it after adding node
// context to the skill's runtime configuration.
func (e *Executor) EventStream() *streampkg.Stream {
	if e == nil || e.runtime == nil {
		return nil
	}
	return e.runtime.EventStream()
}

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

// Execute runs the task. When [Executor.RunE] is set it is invoked directly;
// otherwise Execute asks the bound runtime skill to return a markdown exchange
// document and wraps plain-text replies into that document format.
func (e *Executor) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*runnerpkg.Node, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			e.Status = StatusFailed
			return nil, nil, err
		}
	}
	e.logf("Executing tasks...")
	e.Status = StatusRunning

	if e.RunE != nil {
		output, newNodes, err := e.RunE(ctx, parentOutputs)
		if err != nil {
			e.Status = StatusFailed
			return nil, nil, err
		}
		e.captureResult(output)
		e.Status = StatusDone
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				e.Status = StatusFailed
				return nil, nil, err
			}
		}
		return output, newNodes, nil
	}
	output, err := e.executeExchange(ctx, parentOutputs)
	if err != nil {
		e.Status = StatusFailed
		return nil, nil, err
	}
	e.captureResult(output)
	e.Status = StatusDone
	return output, nil, nil
}

// Polish satisfies the runner's polish contract. The default implementation
// refreshes the planner via [Executor.PolishPlan] and returns a markdown
// exchange document for orchestrator review.
func (e *Executor) Polish(ctx context.Context) (any, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if err := e.PolishPlan(); err != nil {
		return nil, err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return e.polishExchange(ctx)
}

// Report satisfies the runner's report contract. The default implementation
// returns a markdown exchange document summarizing the execution output.
func (e *Executor) Report(ctx context.Context, output any) (any, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return e.reportExchange(ctx, output)
}

// BindForRunner binds the executor's skill for a runner-owned session.
//
// accessibleDirs extends the skill workspace for this runtime binding, so
// request artifact roots and upstream exchange resources can be read or written
// by standard skill tools. The returned string is the bound conversation session
// id, when session persistence is configured.
func (e *Executor) BindForRunner(sessID *string, accessibleDirs []string, sessStores ...llm.SessionStore) (string, error) {
	if e.skill == nil {
		return "", fmt.Errorf("orchestrate: executor requires a non-nil skill")
	}
	sk := e.skill
	if len(accessibleDirs) > 0 {
		dirs := append(e.skill.AccessibleDirs(), accessibleDirs...)
		var err error
		sk, err = e.skill.SetAccessibleDirsAndBuiltinTools(dirs...)
		if err != nil {
			return "", err
		}
	}
	bindOpts := []llm.BindOption(nil)
	if sessID != nil {
		bindOpts = append(bindOpts, llm.WithExistingSession(*sessID, sessStores...))
	} else if len(sessStores) > 0 {
		bindOpts = append(bindOpts, llm.WithNewSession(sessStores...))
	}
	cfg := e.runtimeConversationConfig()
	bound, err := sk.Bind(e.bindClient, cfg, bindOpts...)
	if err != nil {
		return "", err
	}
	e.runtime = bound
	e.accessibleDirs = append([]string(nil), accessibleDirs...)
	if conv := bound.Conversation(); conv != nil {
		return conv.SessionID(), nil
	}
	return "", nil
}

func (e *Executor) runtimeConversationConfig() llm.ConversationConfig {
	cfg := cloneConversationConfig(e.bindConfig)
	if cfg.StreamEvents {
		cfg.EventStream = nil
		cfg.StreamSource = streampkg.Source{Layer: "skill", ID: e.skill.Name, ParentID: e.planner.id}
		cfg.StreamScope = streampkg.Scope{NodeID: e.planner.id}
		cfg.StreamMetadata = map[string]any{
			"node_id":    e.planner.id,
			"skill_name": e.skill.Name,
		}
	}
	return cfg
}

func (e *Executor) runtimeSkill() (*llm.Skill, error) {
	if e.runtime == nil {
		return nil, fmt.Errorf("orchestrate: executor skill %q has not been bound by the runner", e.skill.Name)
	}
	return e.runtime, nil
}

func (e *Executor) captureResult(output any) {
	switch result := output.(type) {
	case *ExecutionResult:
		e.Result = result
	case ExecutionResult:
		copyResult := result
		e.Result = &copyResult
	}
}

func (e *Executor) logf(format string, args ...any) {
	if e.logger == nil {
		return
	}
	e.logger.Debug(fmt.Sprintf(format, args...))
}
