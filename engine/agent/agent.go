package agent

import (
	"context"
	"fmt"
	"log/slog"

	runnerpkg "github.com/tsumina/dango/engine/runner"
	"github.com/tsumina/dango/llm"
	"github.com/tsumina/dango/logging"
	streampkg "github.com/tsumina/dango/stream"
)

// Status reports the lifecycle state of an [Agent].
type Status int

const (
	StatusPending Status = iota
	StatusRunning
	StatusDone
	StatusFailed
)

// ExecutionPlanner carries the working description, reasoning, and proposed
// solution for a task that an [Agent] is about to run. It is mutated in
// place by planning steps such as [Agent.PolishPlan].
type ExecutionPlanner struct {
	// ID is the plan-node identifier the orchestrator assigns when it builds
	// the planner. It is internal correlation state, not part of the planner's
	// serialized task content, so it is excluded from JSON/YAML.
	ID              string `json:"-" yaml:"-"`
	TaskDescription string `json:"task_description" yaml:"description"`
	SourceInput     string `json:"source_input,omitempty" yaml:"source_input,omitempty"`
	ArtifactsDir    string `json:"artifacts_dir,omitempty" yaml:"artifacts_dir,omitempty"`
	Reason          string `json:"reason" yaml:"reason"`
	Solution        string `json:"solution" yaml:"solution"`
	Version         uint32 `json:"version" yaml:"version"`
}

// ExecutionResult is the structured outcome an [Agent] produces after a
// task finishes, including any data it wants to share with downstream nodes.
type ExecutionResult struct {
	Success    bool         `json:"success" yaml:"success"`
	Message    string       `json:"message" yaml:"message"`
	Handoff    bool         `json:"handoff" yaml:"handoff"`
	SharedData []SharedData `json:"shared_data,omitempty" yaml:"shared_data,omitempty"`
}

// SharedData describes a single artifact an [Agent] hands off to other
// tasks via [ExecutionResult.SharedData].
type SharedData struct {
	FilePath    string `json:"file_path" yaml:"file_path"`
	Description string `json:"description" yaml:"description"`
}

// Agent runs a single task on top of a loaded [llm.Skill].
//
// An Agent is the engine-owned proxy container for one Skill runtime. It is
// bound to one Skill at construction time, adds node/agent context to the
// skill's runtime stream configuration, and uses the Skill's directory, prompt,
// tools, and bound LLM client to plan and run the task. The zero value is not
// usable; construct instances with [NewAgent].
type Agent struct {
	logger     *slog.Logger
	skill      *llm.Skill
	planner    *ExecutionPlanner
	bindClient *llm.Client
	bindConfig llm.ConversationConfig
	runtimeCfg llm.ToolSetConfig
	runtime    *llm.Skill
	// runtimePaths is the runner-owned workspace context most recently passed by
	// the runner for this agent's runtime skill.
	runtimePaths runnerpkg.AgentRuntimePaths

	// Result holds the structured outcome of the most recent execution.
	Result *ExecutionResult
	// Status reports the agent's current lifecycle state.
	Status Status

	// RunE optionally overrides the default execution path. It is the
	// hook the runner tests use to inject behavior into an Agent
	// without depending on a real skill or LLM client.
	RunE func(ctx context.Context, parentOutputs map[string]any) (output any, newNodes []*runnerpkg.Node, err error)
}

// AgentOption adjusts a constructed [Agent] before it is returned.
type AgentOption func(*Agent)

// WithAgentLogger installs logger as the Agent's lifecycle logger.
//
// Orchestrator-built agents receive the orchestrator's logger automatically;
// tests use this option directly to inject a buffer-backed logger. The option
// is named separately from the orchestrator's [WithLogger] because Go does
// not allow two functions named WithLogger in the same package even when
// their option types differ.
//
// The Agent keeps a reference to logger. slog.Logger values are safe for
// concurrent use; callers that wrap a handler with additional mutable state are
// responsible for that handler's synchronization.
func WithAgentLogger(logger *slog.Logger) AgentOption {
	return func(e *Agent) {
		if logger != nil {
			e.logger = logger
		}
	}
}

// WithAgentClient installs client as the LLM client forwarded to
// [llm.Skill.Bind].
//
// The Agent keeps a reference to client and uses it when a runner binds the
// skill. [llm.Client] is safe for concurrent request use, but callers must not
// mutate the shared client or its raw SDK client while runner work is in flight.
func WithAgentClient(client *llm.Client) AgentOption {
	return func(e *Agent) {
		e.bindClient = client
	}
}

// NewAgent constructs an [Agent] bound to sk and planner.
//
// sk and planner must be non-nil. cfg is later forwarded to [llm.Skill.Bind]
// by runner-owned execution setup.
func NewAgent(sk *llm.Skill, planner *ExecutionPlanner, cfg llm.ConversationConfig, opts ...AgentOption) (*Agent, error) {
	if sk == nil {
		return nil, fmt.Errorf("orchestrate: agent requires a non-nil skill")
	}
	if planner == nil {
		return nil, fmt.Errorf("orchestrate: agent requires a non-nil planner")
	}
	e := &Agent{
		logger:     logging.NewLogger(logging.DefaultConfig()),
		skill:      sk,
		planner:    planner,
		bindConfig: cfg.Clone(),
		runtimeCfg: sk.ToolSetConfig(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	e.logger.Info("Creating a new Agent")
	return e, nil
}

// Skill returns the [llm.Skill] this agent was bound to.
func (e *Agent) Skill() *llm.Skill { return e.skill }

// Planner returns the [ExecutionPlanner] this agent mutates during
// planning.
func (e *Agent) Planner() *ExecutionPlanner { return e.planner }

// LLMClient returns the effective client this agent will use for skill-run
// stages.
func (e *Agent) LLMClient() *llm.Client {
	if e.runtime != nil {
		return e.runtime.Client()
	}
	return e.bindClient
}

// EventStream returns the bound runtime skill's progress stream. The stream is
// created by the skill binding; the agent exposes it after adding node
// context to the skill's runtime configuration.
func (e *Agent) EventStream() *streampkg.Stream {
	if e == nil || e.runtime == nil {
		return nil
	}
	return e.runtime.EventStream()
}

// PolishPlan refines the planner's reasoning and solution based on the
// current task description. It bumps [ExecutionPlanner.Version] on success.
func (e *Agent) PolishPlan() error {
	e.logf("Planning tasks...")

	if err := e.planTask(); err != nil {
		e.logf("Error planning tasks: %v", err)
		return err
	}
	return nil
}

func (e *Agent) planTask() error {
	e.logf("Planning a task...")

	e.planner.Reason = "The task requires processing data and generating a report."
	e.planner.Solution = "Use a data processing library to analyze the data and generate the report."
	e.planner.Version++
	return nil
}

// BindForRunner binds the agent's skill for a runner-owned session.
//
// runtimePaths carries the runner-owned workspace context for this binding.
// runtimePaths.AccessibleDirs extends the skill workspace so request artifact
// roots and runner-managed channel directories can be read or written by
// standard skill tools. The returned string is the bound conversation session
// id, when session persistence is configured.
func (e *Agent) BindForRunner(sessID *string, runtimePaths runnerpkg.AgentRuntimePaths, sessStores ...llm.SessionStore) (string, error) {
	if e.skill == nil {
		return "", fmt.Errorf("orchestrate: agent requires a non-nil skill")
	}
	sk, err := e.skill.WithToolSetConfig(e.runtimeCfg)
	if err != nil {
		return "", err
	}
	if len(runtimePaths.AccessibleDirs) > 0 {
		dirs := append(e.skill.AccessibleDirs(), runtimePaths.AccessibleDirs...)
		sk, err = sk.SetAccessibleDirsAndBuiltinTools(dirs...)
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
	e.runtimePaths = cloneAgentRuntimePaths(runtimePaths)
	if conv := bound.Conversation(); conv != nil {
		return conv.SessionID(), nil
	}
	return "", nil
}

// RuntimeToolSetConfig returns the per-agent tool policy snapshot used for
// later runner-owned binds.
func (e *Agent) RuntimeToolSetConfig() llm.ToolSetConfig {
	if e == nil {
		return llm.ToolSetConfig{}
	}
	return cloneAgentToolSetConfig(e.runtimeCfg)
}

// SetRuntimeToolSetConfig replaces the per-agent tool policy snapshot used for
// later runner-owned binds.
func (e *Agent) SetRuntimeToolSetConfig(cfg llm.ToolSetConfig) {
	if e == nil {
		return
	}
	e.runtimeCfg = cloneAgentToolSetConfig(cfg)
}

func cloneAgentToolSetConfig(cfg llm.ToolSetConfig) llm.ToolSetConfig {
	cfg.BashAllow = append([]string(nil), cfg.BashAllow...)
	cfg.BashBlock = append([]string(nil), cfg.BashBlock...)
	cfg.Extras = append([]llm.ExtraTool(nil), cfg.Extras...)
	if cfg.Policies != nil {
		cloned := make(map[llm.CapabilityRef]llm.ExecPolicy, len(cfg.Policies))
		for k, v := range cfg.Policies {
			cloned[k] = v
		}
		cfg.Policies = cloned
	}
	cfg.BashCommandPolicies = append([]llm.BashCommandPolicy(nil), cfg.BashCommandPolicies...)
	for i := range cfg.BashCommandPolicies {
		cfg.BashCommandPolicies[i].ArgsPrefix = append([]string(nil), cfg.BashCommandPolicies[i].ArgsPrefix...)
	}
	return cfg
}

func (e *Agent) runtimeConversationConfig() llm.ConversationConfig {
	cfg := e.bindConfig.Clone()
	if cfg.StreamEvents {
		cfg.EventStream = nil
		cfg.StreamSource = streampkg.Source{Layer: "skill", ID: e.skill.Name, ParentID: e.planner.ID}
		cfg.StreamScope = streampkg.Scope{NodeID: e.planner.ID}
		cfg.StreamMetadata = map[string]any{
			"node_id":    e.planner.ID,
			"skill_name": e.skill.Name,
		}
	}
	return cfg
}

func (e *Agent) runtimeSkill() (*llm.Skill, error) {
	if e.runtime == nil {
		return nil, fmt.Errorf("orchestrate: agent skill %q has not been bound by the runner", e.skill.Name)
	}
	return e.runtime, nil
}

func (e *Agent) captureResult(output any) {
	switch result := output.(type) {
	case *ExecutionResult:
		e.Result = result
	case ExecutionResult:
		copyResult := result
		e.Result = &copyResult
	}
}

func (e *Agent) logf(format string, args ...any) {
	e.logger.Debug(fmt.Sprintf(format, args...))
}
