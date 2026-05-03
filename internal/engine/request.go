package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
)

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
// caller. It contains only caller-provided input; observation state such as the
// request stream is returned in [Response].
type Request struct {
	Input        string          `json:"input" yaml:"input"`
	Priority     RequestPriority `json:"priority,omitempty" yaml:"priority,omitempty"`
	ArtifactsDir string          `json:"artifacts_dir,omitempty" yaml:"artifacts_dir,omitempty"`
}

// Response is returned by [Orchestrator.StartRequest].
//
// Stream is the request-scoped event stream created for this orchestration
// attempt. RunnerID is populated once planning succeeds and a runner is
// materialized; it is empty when the request is rejected before runner
// creation.
type Response struct {
	Stream   *streampkg.Stream
	RunnerID string
}

// RejectReason explains why a request cannot currently be turned into a plan.
type RejectReason struct {
	Summary       string   `json:"summary" yaml:"summary"`
	Analysis      string   `json:"analysis" yaml:"analysis"`
	MissingSkills []string `json:"missing_skills,omitempty" yaml:"missing_skills,omitempty"`
}

// StartRequest is the outer-facing request entrypoint.
//
// It plans and materializes a runner, stores it for query and stream APIs,
// subscribes to its lifecycle updates, and then either starts it immediately
// or queues it when the configured runner limit is full. StartRequest is
// non-blocking and returns a response containing the request stream and runner
// ID once the runner has been accepted into orchestration. The stream is
// replayable, so callers may subscribe after StartRequest returns and still
// inspect planning events emitted during request startup.
func (o *Orchestrator) StartRequest(ctx context.Context, req Request) (*Response, error) {
	ctx = o.operationContext(ctx)
	if !req.Priority.valid() {
		return nil, fmt.Errorf("orchestrate: request priority must be between %d and %d", RequestPriorityDefault, RequestPriorityHighest)
	}
	requestStream := streampkg.New(streampkg.Scope{})
	resp := &Response{Stream: requestStream}
	var streamMerges []*streampkg.Merge
	cleanupMerges := true
	defer func() {
		if cleanupMerges {
			stopStreamMerges(streamMerges)
		}
	}()

	o.mu.Lock()
	o.configLocked = true
	logger := o.logger
	orchestratorSkill := o.orchestratorSkill
	runnerStore := o.runnerStore
	skillConfigs := cloneAddSkillConfigs(o.skills)
	o.mu.Unlock()
	envClient, envClientErr := o.resolveEnvClient()
	planningSkill, err := runtimeOrchestrator(orchestratorSkill, envClient, envClientErr, planningConversationConfig())
	if err != nil {
		if errors.Is(err, errOrchestratorSkillUnconfigured) {
			return resp, &RequestRejectedError{Reason: rejectUnconfiguredPlan(&req, cloneSkillMap(skillConfigs), orchestratorSkill)}
		}
		return resp, err
	}
	if merge, err := mergeChildStream(ctx, requestStream, planningSkill.EventStream()); err != nil {
		return resp, err
	} else if merge != nil {
		streamMerges = append(streamMerges, merge)
	}
	emitEngineStreamEvent(ctx, requestStream,
		streamSourceOrchestrator(),
		streampkg.EventStatusStarted,
		streampkg.StatusRunning,
		"orchestrator planning started",
		streampkg.Scope{},
		nil,
	)

	skillSummaries := collectSkillSummaries(cloneSkillMap(skillConfigs))
	plan, reject, err := planWithOrchestrator(ctx, req, skillSummaries, planningSkill, requestStream)
	if err != nil {
		emitEngineStreamEvent(ctx, requestStream,
			streamSourceOrchestrator(),
			streampkg.EventStatusFailed,
			streampkg.StatusFailed,
			err.Error(),
			streampkg.Scope{},
			nil,
		)
		return resp, err
	}
	if reject != nil {
		emitEngineStreamEvent(ctx, requestStream,
			streamSourceOrchestrator(),
			streampkg.EventStatusFailed,
			streampkg.StatusFailed,
			reject.Summary,
			streampkg.Scope{},
			map[string]any{"reason": reject.Analysis},
		)
		return resp, &RequestRejectedError{Reason: reject}
	}
	if plan == nil {
		return resp, fmt.Errorf("orchestrate: planner returned neither a plan nor a reject reason")
	}
	plannerSkill, err := bindOrchestratorSkillWithConfig(orchestratorSkill, planningSkill.Client(), runnerPlannerConversationConfig())
	if err != nil {
		return resp, err
	}
	if merge, err := mergeChildStream(ctx, requestStream, plannerSkill.EventStream()); err != nil {
		return resp, err
	} else if merge != nil {
		streamMerges = append(streamMerges, merge)
	}

	runner, err := newRunnerFromPlan(ctx, logger, runnerStore, req, plan, skillConfigs, plannerSkill, skillSummaries)
	if err != nil {
		return resp, err
	}
	if merge, err := mergeChildStream(ctx, requestStream, runner.EventStream()); err != nil {
		return resp, err
	} else if merge != nil {
		streamMerges = append(streamMerges, merge)
	}
	runnerID := runner.ID()
	emitEngineStreamEvent(ctx, requestStream,
		streamSourceOrchestrator(),
		streampkg.EventStatusProgress,
		streampkg.StatusRunning,
		map[string]any{"message": "runner created", "runner_id": runnerID},
		streampkg.Scope{RunnerID: runnerID},
		nil,
	)

	o.mu.Lock()
	o.runners[runnerID] = runner
	o.mu.Unlock()

	if err := o.submitManagedRunner(ctx, runner, req.Priority); err != nil {
		return resp, err
	}
	cleanupMerges = false
	go o.watchRunnerDone(runner)
	resp.RunnerID = runnerID
	return resp, nil
}

func streamSourceOrchestrator() streampkg.Source {
	return streampkg.Source{Layer: "orchestrator", ID: "orchestrator"}
}

func emitEngineStreamEvent(ctx context.Context, eventStream *streampkg.Stream, source streampkg.Source, eventType string, status string, delta any, scope streampkg.Scope, metadata map[string]any) {
	if eventStream == nil {
		return
	}
	raw, err := json.Marshal(delta)
	if err != nil {
		raw, _ = json.Marshal(fmt.Sprint(delta))
	}
	_ = eventStream.Emit(ctx, streampkg.Event{
		EventType: eventType,
		From:      source,
		Status:    status,
		Delta:     json.RawMessage(raw),
		Scope:     scope,
		Metadata:  metadata,
	})
}

func mergeChildStream(ctx context.Context, downstream *streampkg.Stream, upstream *streampkg.Stream) (*streampkg.Merge, error) {
	if downstream == nil || upstream == nil {
		return nil, nil
	}
	merge, err := downstream.MergeFrom(ctx, upstream, streampkg.Filter{}, streampkg.WithSubscriberBuffer(4096))
	if err != nil {
		return nil, fmt.Errorf("orchestrate: merge child stream: %w", err)
	}
	return merge, nil
}

func stopStreamMerges(merges []*streampkg.Merge) {
	for _, merge := range merges {
		if merge != nil {
			merge.Stop()
		}
	}
}

func newRunnerFromPlan(ctx context.Context, logger *slog.Logger, store runnerpkg.RunnerStore, req Request, plan *CoarsePlan, skills map[string]AddSkillConfig, plannerSkill *llm.Skill, skillSummaries []runnerpkg.SkillSummary) (*runnerpkg.Runner, error) {
	nodes, err := buildPlanNodes(logger, req, plan, skills)
	if err != nil {
		return nil, err
	}
	return runnerpkg.NewWithSetup(runnerpkg.Setup{
		Context:        ctx,
		Logger:         logger,
		Store:          store,
		Plan:           plan,
		Nodes:          nodes,
		PlannerSkill:   plannerSkill,
		SkillSummaries: skillSummaries,
		PlanNodeBuilder: func(replanned *runnerpkg.CoarsePlan) (map[string]*runnerpkg.Node, error) {
			request := Request{Input: replanned.Request, ArtifactsDir: req.ArtifactsDir}
			return buildPlanNodes(logger, request, replanned, skills)
		},
	}), nil
}

func buildPlanNodes(logger *slog.Logger, req Request, plan *CoarsePlan, skills map[string]AddSkillConfig) (map[string]*runnerpkg.Node, error) {
	if len(plan.Nodes) == 0 {
		return nil, fmt.Errorf("orchestrate: coarse plan must contain at least one node")
	}

	nodes := make(map[string]*runnerpkg.Node, len(plan.Nodes))
	for _, step := range plan.Nodes {
		if step.ID == "" {
			return nil, fmt.Errorf("orchestrate: coarse plan node has empty id")
		}
		if _, exists := nodes[step.ID]; exists {
			return nil, fmt.Errorf("orchestrate: coarse plan node %q is duplicated", step.ID)
		}
		if step.SkillName == "" {
			return nil, fmt.Errorf("orchestrate: coarse plan node %q has empty skill name", step.ID)
		}
		skillCfg, ok := skills[step.SkillName]
		if !ok || skillCfg.Skill == nil {
			return nil, fmt.Errorf("orchestrate: coarse plan node %q references unregistered skill %q", step.ID, step.SkillName)
		}

		planner := &ExecutionPlanner{
			id:              step.ID,
			TaskDescription: step.TaskDescription,
			SourceInput:     req.Input,
			ArtifactsDir:    req.ArtifactsDir,
		}
		if planner.TaskDescription == "" {
			planner.TaskDescription = req.Input
		}
		skill := skillCfg.Skill
		if req.ArtifactsDir != "" {
			dirs := append([]string(nil), skillCfg.AccessibleDirs...)
			dirs = append(dirs, req.ArtifactsDir)
			var err error
			skill, err = skill.WithAccessibleDirsAndBuiltinTools(dirs...)
			if err != nil {
				return nil, fmt.Errorf("orchestrate: configure artifacts dir for node %q: %w", step.ID, err)
			}
		}
		convCfg := conversationConfigForNode(skillCfg.Config, step.ID, step.SkillName)
		executor, err := NewExecutor(logger, skill, skillCfg.Client, convCfg, planner)
		if err != nil {
			return nil, fmt.Errorf("orchestrate: build executor for node %q: %w", step.ID, err)
		}

		nodes[step.ID] = &runnerpkg.Node{
			Id:              step.ID,
			SkillName:       step.SkillName,
			TaskDescription: planner.TaskDescription,
			Executor:        executor,
		}
	}

	for _, step := range plan.Nodes {
		node := nodes[step.ID]
		for _, parentID := range step.DependsOn {
			parent := nodes[parentID]
			if parent == nil {
				return nil, fmt.Errorf("orchestrate: coarse plan node %q depends on unknown node %q", step.ID, parentID)
			}
			node.Parents = append(node.Parents, parent)
		}
	}

	return nodes, nil
}

func conversationConfigForNode(cfg *llm.ConversationConfig, nodeID string, skillName string) *llm.ConversationConfig {
	out := cloneConversationConfig(cfg)
	if out == nil {
		out = &llm.ConversationConfig{}
	}
	out.StreamEvents = true
	out.StreamSource = streampkg.Source{Layer: "skill", ID: skillName, ParentID: nodeID}
	out.StreamScope = streampkg.Scope{NodeID: nodeID}
	out.StreamMetadata = map[string]any{
		"node_id":    nodeID,
		"skill_name": skillName,
	}
	return out
}

func cloneAddSkillConfigs(skills map[string]AddSkillConfig) map[string]AddSkillConfig {
	copyMap := make(map[string]AddSkillConfig, len(skills))
	for name, cfg := range skills {
		copyMap[name] = AddSkillConfig{
			Skill:          cfg.Skill,
			AccessibleDirs: append([]string(nil), cfg.AccessibleDirs...),
			Client:         cfg.Client,
			Config:         cloneConversationConfig(cfg.Config),
		}
	}
	return copyMap
}

func cloneSkillMap(skills map[string]AddSkillConfig) map[string]*llm.Skill {
	copyMap := make(map[string]*llm.Skill, len(skills))
	for name, cfg := range skills {
		copyMap[name] = cfg.Skill
	}
	return copyMap
}

func rejectUnconfiguredPlan(req *Request, skills map[string]*llm.Skill, orchestratorSkill *llm.Skill) *RejectReason {
	return &RejectReason{
		Summary:  "task cannot proceed",
		Analysis: "the orchestrator skill is not bound to an llm client, so the request cannot be planned yet",
	}
}
