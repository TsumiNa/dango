package orchestrate

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/tsumina/dango/internal/llm/skill"
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
	plan, reject, err := o.planFromRequest(req)
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

// planFromRequest asks the configured planner to analyze req against the
// registered skills.
//
// When the planner rejects the request, planFromRequest returns the rejection
// details without creating a runner. When the planner returns a coarse plan,
// planFromRequest materializes the plan into Executors and Nodes, creates a new
// runner for them, stores that runner inside the Orchestrator, and returns the
// plan annotated with the runner ID.
func (o *Orchestrator) planFromRequest(req *Request) (coarsePlan *CoarsePlan, rejectReason *RejectReason, err error) {
	if req == nil {
		return nil, nil, fmt.Errorf("orchestrate: nil request")
	}

	o.mu.Lock()
	o.configLocked = true
	logger := o.logger
	planFn := o.planFn
	runnerStore := o.runnerStore
	skills := cloneSkillMap(o.skills)
	o.mu.Unlock()

	plan, reject, err := planFn(req, skills)
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

	managedRunner, err := buildManagedRunner(logger, runnerStore, req, plan, skills)
	if err != nil {
		return nil, nil, err
	}
	managedRunner.onExecutionDrained = func() {
		o.releaseRunnerExecutionSlot(managedRunner.Runner.ID())
	}
	plan.RunnerID = managedRunner.Runner.ID()

	o.mu.Lock()
	o.runners[plan.RunnerID] = managedRunner
	o.mu.Unlock()

	return plan, nil, nil
}

func buildManagedRunner(logger *slog.Logger, store runnerpkg.RunnerStore, req *Request, plan *CoarsePlan, skills map[string]*skill.Skill) (*ManagedRunner, error) {
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
		executor, err := NewExecutor(logger, sk, planner)
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

	runner := runnerpkg.NewRunner(logger)
	if err := runner.SetStore(store); err != nil {
		return nil, fmt.Errorf("orchestrate: configure runner store: %w", err)
	}
	return newManagedRunner(runner, plan, nodes), nil
}

func cloneSkillMap(skills map[string]*skill.Skill) map[string]*skill.Skill {
	copyMap := make(map[string]*skill.Skill, len(skills))
	for name, sk := range skills {
		copyMap[name] = sk
	}
	return copyMap
}

func rejectUnconfiguredPlan(req *Request, skills map[string]*skill.Skill) (*CoarsePlan, *RejectReason, error) {
	return nil, &RejectReason{
		Summary:  "task cannot proceed",
		Analysis: "no planning function is configured to map the request onto the registered skill set",
	}, nil
}
