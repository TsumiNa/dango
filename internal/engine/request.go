package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
)

const runnerRequestMergeWindow = 10 * time.Millisecond

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
// attempt. StartRequest returns before planning finishes, so RunnerID is empty
// in the initial response. The materialized runner ID is emitted on Stream in
// the orchestrator "runner created" progress event.
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

type requestStartup struct {
	logger            *slog.Logger
	orchestratorSkill *llm.Skill
	runnerStore       runnerpkg.RunnerStore
	skillConfigs      map[string]SkillRegistration
}

// StartRequest is the outer-facing request entrypoint.
//
// It returns a request stream immediately, then plans and materializes the
// runner in the background. Planning, rejection, runner creation, and runner
// lifecycle updates are communicated through the stream. The stream is
// replayable, so callers may subscribe after StartRequest returns and still
// inspect events emitted during request startup. StartRequest must not become a
// synchronization point for planner, runner, executor, or skill work; callers
// that need to wait for progress should subscribe to the returned stream or use
// explicit query APIs.
func (o *Orchestrator) StartRequest(ctx context.Context, req Request) (*Response, error) {
	ctx = o.operationContext(ctx)
	if !req.Priority.valid() {
		return nil, fmt.Errorf("orchestrate: request priority must be between %d and %d", RequestPriorityDefault, RequestPriorityHighest)
	}
	requestStream := streampkg.New(streampkg.Scope{}, streampkg.DefaultConfig())

	o.mu.Lock()
	o.configLocked = true
	startup := requestStartup{
		logger:            o.logger,
		orchestratorSkill: o.orchestratorSkill,
		runnerStore:       o.runnerStore,
		skillConfigs:      cloneSkillRegistrations(o.skills),
	}
	o.mu.Unlock()

	go func() {
		if _, err := o.startRequestWithStream(ctx, req, requestStream, startup); err != nil {
			emitEngineStreamEvent(ctx, requestStream,
				streamSourceOrchestrator(),
				streampkg.EventStatusFailed,
				streampkg.StatusFailed,
				err.Error(),
				streampkg.Scope{},
				nil,
			)
		}
	}()
	return &Response{Stream: requestStream}, nil
}

func (o *Orchestrator) startRequestWithStream(ctx context.Context, req Request, requestStream *streampkg.Stream, startup requestStartup) (*Response, error) {
	resp := &Response{Stream: requestStream}
	var streamMerges []*streampkg.Merge
	cleanupMerges := true
	defer func() {
		if cleanupMerges {
			stopStreamMerges(streamMerges)
		}
	}()

	envClient, envClientErr := o.resolveEnvClient()
	planningSkill, err := runtimeOrchestrator(startup.orchestratorSkill, envClient, envClientErr, planningConversationConfig())
	if err != nil {
		if errors.Is(err, errOrchestratorSkillUnconfigured) {
			return resp, &RequestRejectedError{Reason: rejectUnconfiguredPlan(&req, cloneSkillMap(startup.skillConfigs), startup.orchestratorSkill)}
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

	skillSummaries := collectSkillSummaries(cloneSkillMap(startup.skillConfigs))
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
	plannerSkill, err := bindOrchestratorSkillWithConfig(startup.orchestratorSkill, planningSkill.Client(), runnerPlannerConversationConfig())
	if err != nil {
		return resp, err
	}
	if merge, err := mergeChildStream(ctx, requestStream, plannerSkill.EventStream()); err != nil {
		return resp, err
	} else if merge != nil {
		streamMerges = append(streamMerges, merge)
	}

	runner, err := newRunnerFromPlan(ctx, startup.logger, startup.runnerStore, req, plan, startup.skillConfigs, plannerSkill, skillSummaries)
	if err != nil {
		return resp, err
	}
	if merge, err := mergeRunnerStream(ctx, requestStream, runner.EventStream()); err != nil {
		return resp, err
	} else if merge != nil {
		streamMerges = append(streamMerges, merge)
	}
	runnerID := runner.ID()
	o.mu.Lock()
	o.runners[runnerID] = runner
	o.mu.Unlock()

	emitEngineStreamEvent(ctx, requestStream,
		streamSourceOrchestrator(),
		streampkg.EventStatusProgress,
		streampkg.StatusRunning,
		map[string]any{"message": "runner created", "runner_id": runnerID},
		streampkg.Scope{RunnerID: runnerID},
		nil,
	)

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

func mergeRunnerStream(ctx context.Context, downstream *streampkg.Stream, upstream *streampkg.Stream) (*streampkg.Merge, error) {
	if downstream == nil || upstream == nil {
		return nil, nil
	}
	merge, err := downstream.MergeFromWithConfig(ctx, upstream, streampkg.Filter{}, streampkg.MergeWindowConfig{
		TickDuration: runnerRequestMergeWindow,
	}, streampkg.WithSubscriberBuffer(4096))
	if err != nil {
		return nil, fmt.Errorf("orchestrate: merge runner stream: %w", err)
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

func newRunnerFromPlan(ctx context.Context, logger *slog.Logger, store runnerpkg.RunnerStore, req Request, plan *CoarsePlan, skills map[string]SkillRegistration, plannerSkill *llm.Skill, skillSummaries []runnerpkg.SkillSummary) (*runnerpkg.Runner, error) {
	nodes, err := buildPlanNodes(logger, req, plan, skills)
	if err != nil {
		return nil, err
	}
	opts := []runnerpkg.Option{
		runnerpkg.WithContext(ctx),
		runnerpkg.WithLogger(logger),
		runnerpkg.WithStore(store),
		runnerpkg.WithInitialPlan(plan, nodes),
		runnerpkg.WithPlannerSkill(plannerSkill),
		runnerpkg.WithSkillSummaries(skillSummaries),
		runnerpkg.WithPlanNodeBuilder(func(replanned *runnerpkg.CoarsePlan) (map[string]*runnerpkg.Node, error) {
			request := Request{Input: replanned.Request, ArtifactsDir: req.ArtifactsDir}
			return buildPlanNodes(logger, request, replanned, skills)
		}),
	}
	if req.ArtifactsDir != "" {
		opts = append(opts, runnerpkg.WithAllowedResourceRoots(req.ArtifactsDir))
	}
	return runnerpkg.New(opts...), nil
}

func buildPlanNodes(logger *slog.Logger, req Request, plan *CoarsePlan, skills map[string]SkillRegistration) (map[string]*runnerpkg.Node, error) {
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
			skill, err = skill.SetAccessibleDirsAndBuiltinTools(dirs...)
			if err != nil {
				return nil, fmt.Errorf("orchestrate: configure artifacts dir for node %q: %w", step.ID, err)
			}
		}
		convCfg := conversationConfigForNode(skillCfg.Config, step.ID, step.SkillName)
		executor, err := NewExecutor(skill, planner, convCfg, WithExecutorLogger(logger), WithExecutorClient(skillCfg.Client))
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

func conversationConfigForNode(cfg llm.ConversationConfig, nodeID string, skillName string) llm.ConversationConfig {
	out := cloneConversationConfig(cfg)
	out.StreamEvents = true
	out.EventStream = nil
	out.StreamMetadata = nil
	return out
}

func cloneSkillRegistrations(skills map[string]SkillRegistration) map[string]SkillRegistration {
	copyMap := make(map[string]SkillRegistration, len(skills))
	for name, cfg := range skills {
		copyMap[name] = SkillRegistration{
			Skill:          cfg.Skill,
			AccessibleDirs: append([]string(nil), cfg.AccessibleDirs...),
			Client:         cfg.Client,
			Config:         cloneConversationConfig(cfg.Config),
		}
	}
	return copyMap
}

func cloneSkillMap(skills map[string]SkillRegistration) map[string]*llm.Skill {
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
