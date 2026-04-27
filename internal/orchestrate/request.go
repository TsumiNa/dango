package orchestrate

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/tsumina/dango/internal/llm"
	runnerpkg "github.com/tsumina/dango/internal/orchestrate/runner"
)

// StartRequest is the outer-facing request entrypoint.
//
// It plans and materializes a runner, then either starts it immediately or
// queues it when the configured runner execution limit is full. Query and
// stream APIs can be used afterwards with the returned RunnerID.
func (o *Orchestrator) StartRequest(ctx context.Context, req *Request) (coarsePlan *CoarsePlan, rejectReason *RejectReason, err error) {
	if req == nil {
		return nil, nil, fmt.Errorf("orchestrate: nil request")
	}
	if !req.Priority.valid() {
		return nil, nil, fmt.Errorf("orchestrate: request priority must be between %d and %d", RequestPriorityDefault, RequestPriorityHighest)
	}
	plan, reject, err := o.planFromRequest(ctx, req)
	if err != nil || reject != nil || plan == nil {
		return plan, reject, err
	}
	runner, err := o.Runner(plan.RunnerID)
	if err != nil {
		return nil, nil, err
	}
	if err := o.submitManagedRunner(ctx, runner, req.Priority); err != nil {
		return nil, nil, err
	}
	return plan, nil, nil
}

// planFromRequest asks the orchestrator-owned skill to analyze req against the
// registered skills.
//
// When the planner rejects the request, planFromRequest returns the rejection
// details without creating a runner. When the planner returns a coarse plan,
// planFromRequest materializes the plan into Executors and Nodes, creates a new
// runner for them, stores that runner inside the Orchestrator, and returns the
// plan annotated with the runner ID.
func (o *Orchestrator) planFromRequest(ctx context.Context, req *Request) (coarsePlan *CoarsePlan, rejectReason *RejectReason, err error) {
	if req == nil {
		return nil, nil, fmt.Errorf("orchestrate: nil request")
	}

	o.mu.Lock()
	o.configLocked = true
	logger := o.logger
	orchestratorSkill := o.orchestratorSkill
	runnerStore := o.runnerStore
	skills := cloneSkillMap(o.skills)
	skillClients := cloneSkillClientFactories(o.skillClientByName)
	o.mu.Unlock()
	envClient, envClientErr := o.resolveEnvClient()

	plan, reject, err := planWithOrchestratorSkill(ctx, req, skills, orchestratorSkill, envClient, envClientErr)
	if err != nil {
		return nil, nil, err
	}
	if plan != nil && reject != nil {
		return nil, nil, fmt.Errorf("orchestrate: planner returned both a plan and a reject reason")
	}
	if reject != nil {
		return nil, reject, nil
	}
	if plan == nil {
		return nil, nil, fmt.Errorf("orchestrate: planner returned neither a plan nor a reject reason")
	}

	runner, err := buildRunner(logger, orchestratorSkill.Client(), runnerStore, req, plan, skills, skillClients)
	if err != nil {
		return nil, nil, err
	}
	plan.RunnerID = runner.ID()

	o.mu.Lock()
	o.runners[plan.RunnerID] = runner
	o.mu.Unlock()

	go o.watchRunnerDone(runner)

	return plan, nil, nil
}

func buildRunner(logger *slog.Logger, client *llm.Client, store runnerpkg.RunnerStore, req *Request, plan *CoarsePlan, skills map[string]*llm.Skill, skillClients map[string]SkillClientFactory) (*runnerpkg.Runner, error) {
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
		sk := skills[step.SkillName]
		if sk == nil {
			return nil, fmt.Errorf("orchestrate: coarse plan node %q references unregistered skill %q", step.ID, step.SkillName)
		}

		planner := &ExecutionPlanner{
			id:              step.ID,
			TaskDescription: step.TaskDescription,
		}
		if planner.TaskDescription == "" {
			planner.TaskDescription = req.Input
		}
		executor, err := NewExecutor(logger, sk, planner, client, skillClients[step.SkillName])
		if err != nil {
			return nil, fmt.Errorf("orchestrate: build executor for node %q: %w", step.ID, err)
		}

		nodes[step.ID] = &runnerpkg.Node{Id: step.ID, Executor: executor}
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

	return runnerpkg.New(
		runnerpkg.WithLogger(logger),
		runnerpkg.WithStore(store),
		runnerpkg.WithPlan(plan, nodes),
	), nil
}

func cloneSkillMap(skills map[string]*llm.Skill) map[string]*llm.Skill {
	copyMap := make(map[string]*llm.Skill, len(skills))
	for name, sk := range skills {
		copyMap[name] = sk
	}
	return copyMap
}

func cloneSkillClientFactories(factories map[string]SkillClientFactory) map[string]SkillClientFactory {
	copyMap := make(map[string]SkillClientFactory, len(factories))
	for name, factory := range factories {
		copyMap[name] = factory
	}
	return copyMap
}

func rejectUnconfiguredPlan(req *Request, skills map[string]*llm.Skill, orchestratorSkill *llm.Skill) (*CoarsePlan, *RejectReason, error) {
	return nil, &RejectReason{
		Summary:  "task cannot proceed",
		Analysis: "the orchestrator skill is not bound to an llm client, so the request cannot be planned yet",
	}, nil
}
