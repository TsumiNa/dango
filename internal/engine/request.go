package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
)

// StartRequest is the outer-facing request entrypoint.
//
// It plans and materializes a runner, stores it for query and stream APIs,
// subscribes to its lifecycle updates, and then either starts it immediately
// or queues it when the configured runner limit is full. StartRequest is
// non-blocking and returns the runner ID once the runner has been accepted
// into orchestration.
func (o *Orchestrator) StartRequest(ctx context.Context, req *Request) (string, error) {
	return o.startRequest(ctx, req, nil)
}

// StartRequestWithProgress is like [Orchestrator.StartRequest], but streams
// orchestrator-owned planning progress through progress before a runner exists.
func (o *Orchestrator) StartRequestWithProgress(ctx context.Context, req *Request, progress OrchestratorProgressFunc) (string, error) {
	return o.startRequest(ctx, req, progress)
}

func (o *Orchestrator) startRequest(ctx context.Context, req *Request, progress OrchestratorProgressFunc) (string, error) {
	ctx = o.operationContext(ctx)
	if req == nil {
		return "", fmt.Errorf("orchestrate: nil request")
	}
	if !req.Priority.valid() {
		return "", fmt.Errorf("orchestrate: request priority must be between %d and %d", RequestPriorityDefault, RequestPriorityHighest)
	}

	o.mu.Lock()
	o.configLocked = true
	logger := o.logger
	orchestratorSkill := o.orchestratorSkill
	runnerStore := o.runnerStore
	skillConfigs := cloneAddSkillConfigs(o.skills)
	o.mu.Unlock()
	envClient, envClientErr := o.resolveEnvClient()
	runtimeSkill, err := runtimeOrchestrator(orchestratorSkill, envClient, envClientErr)
	if err != nil {
		if errors.Is(err, errOrchestratorSkillUnconfigured) {
			return "", &RequestRejectedError{Reason: rejectUnconfiguredPlan(req, cloneSkillMap(skillConfigs), orchestratorSkill)}
		}
		return "", err
	}
	emitEngineStreamEvent(ctx, req.Stream,
		streamSourceOrchestrator(),
		streampkg.EventStatusStarted,
		streampkg.StatusRunning,
		"orchestrator planning started",
		streampkg.Scope{},
		nil,
	)

	skillSummaries := collectSkillSummaries(cloneSkillMap(skillConfigs))
	plan, reject, err := planWithOrchestrator(ctx, req, skillSummaries, runtimeSkill, req.Stream, progress)
	if err != nil {
		emitEngineStreamEvent(ctx, req.Stream,
			streamSourceOrchestrator(),
			streampkg.EventStatusFailed,
			streampkg.StatusFailed,
			err.Error(),
			streampkg.Scope{},
			nil,
		)
		return "", err
	}
	if reject != nil {
		emitEngineStreamEvent(ctx, req.Stream,
			streamSourceOrchestrator(),
			streampkg.EventStatusFailed,
			streampkg.StatusFailed,
			reject.Summary,
			streampkg.Scope{},
			map[string]any{"reason": reject.Analysis},
		)
		return "", &RequestRejectedError{Reason: reject}
	}
	if plan == nil {
		return "", fmt.Errorf("orchestrate: planner returned neither a plan nor a reject reason")
	}

	runner, err := newRunnerFromPlan(ctx, logger, runnerStore, req, plan, skillConfigs, runtimeSkill, skillSummaries)
	if err != nil {
		return "", err
	}
	runnerID := runner.ID()
	emitEngineStreamEvent(ctx, req.Stream,
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

	go o.watchRunnerDone(runner)
	if err := o.submitManagedRunner(ctx, runner, req.Priority); err != nil {
		return "", err
	}
	return runnerID, nil
}

func newRunnerFromPlan(ctx context.Context, logger *slog.Logger, store runnerpkg.RunnerStore, req *Request, plan *CoarsePlan, skills map[string]AddSkillConfig, plannerSkill *llm.Skill, skillSummaries []runnerpkg.SkillSummary) (*runnerpkg.Runner, error) {
	nodes, err := buildPlanNodes(logger, req, plan, skills)
	if err != nil {
		return nil, err
	}
	return runnerpkg.New(
		runnerpkg.WithContext(ctx),
		runnerpkg.WithLogger(logger),
		runnerpkg.WithStore(store),
		runnerpkg.WithStream(req.Stream),
		runnerpkg.WithPlan(plan, nodes),
		runnerpkg.WithPlannerSkill(plannerSkill),
		runnerpkg.WithSkillSummaries(skillSummaries),
		runnerpkg.WithPlanNodeBuilder(func(replanned *runnerpkg.CoarsePlan) (map[string]*runnerpkg.Node, error) {
			request := &Request{Input: replanned.Request, ArtifactsDir: req.ArtifactsDir, Stream: req.Stream}
			return buildPlanNodes(logger, request, replanned, skills)
		}),
	), nil
}

func buildPlanNodes(logger *slog.Logger, req *Request, plan *CoarsePlan, skills map[string]AddSkillConfig) (map[string]*runnerpkg.Node, error) {
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
			ArtifactsDir:    req.ArtifactsDir,
		}
		if planner.TaskDescription == "" {
			planner.TaskDescription = req.Input
		}
		convCfg := conversationConfigForNode(skillCfg.Config, req.Stream, step.ID, step.SkillName)
		executor, err := NewExecutor(logger, skillCfg.Skill, skillCfg.Client, convCfg, planner)
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

func conversationConfigForNode(cfg *llm.ConversationConfig, eventStream *streampkg.Stream, nodeID string, skillName string) *llm.ConversationConfig {
	out := cloneConversationConfig(cfg)
	if eventStream == nil {
		return out
	}
	if out == nil {
		out = &llm.ConversationConfig{}
	}
	out.Stream = eventStream
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
