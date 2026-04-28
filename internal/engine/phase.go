package engine

import (
	"context"
	"errors"
	"fmt"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
)

// ReviewRunnerPlan asks the orchestrator-owned skill to review the runner's
// current polished plan and return an approval decision.
func (o *Orchestrator) ReviewRunnerPlan(ctx context.Context, id string) (*PlanReview, error) {
	runner, err := o.Runner(id)
	if err != nil {
		return nil, err
	}
	if runner.Phase() != runnerpkg.PhaseAwaitingReview {
		return nil, ErrRunnerPlanNotAwaitingReview
	}
	o.mu.RLock()
	orchestratorSkill := o.orchestratorSkill
	o.mu.RUnlock()
	envClient, envClientErr := o.resolveEnvClient()
	return reviewWithOrchestratorSkill(ctx, runner.Plan(), runner.PolishFragments(), orchestratorSkill, envClient, envClientErr)
}

// AcceptRunnerPlan adopts the reviewed plan for the identified runner and
// starts execution.
func (o *Orchestrator) AcceptRunnerPlan(ctx context.Context, id string, plan *CoarsePlan) error {
	runner, err := o.Runner(id)
	if err != nil {
		return err
	}
	if runner.Phase() != runnerpkg.PhaseAwaitingReview {
		return ErrRunnerPlanNotAwaitingReview
	}
	if plan == nil {
		plan = runner.Plan()
	}
	o.mu.Lock()
	if o.maxRunningRunners > 0 {
		if _, ok := o.runningRunnerIDs[id]; !ok && len(o.runningRunnerIDs) >= o.maxRunningRunners {
			o.mu.Unlock()
			return ErrRunnerExecutionSlotsFull
		}
		o.runningRunnerIDs[id] = struct{}{}
	}
	o.mu.Unlock()
	if err := o.startManagedRunnerWithReservedSlot(ctx, runner, plan); err != nil {
		o.mu.Lock()
		delete(o.runningRunnerIDs, id)
		o.mu.Unlock()
		if errors.Is(err, runnerpkg.ErrInvalidPhase) {
			return ErrRunnerPlanNotAwaitingReview
		}
		return err
	}
	return nil
}

// RejectRunnerPlan rejects the reviewed plan for the identified runner and
// returns it to the replanning state.
func (o *Orchestrator) RejectRunnerPlan(ctx context.Context, id string, reason string) error {
	runner, err := o.Runner(id)
	if err != nil {
		return err
	}
	if reason == "" {
		review, err := o.ReviewRunnerPlan(ctx, id)
		if err != nil {
			return err
		}
		if review.Approved {
			return fmt.Errorf("orchestrate: auto review approved the plan; provide an explicit rejection reason")
		}
		reason = review.Reason
		if reason == "" {
			reason = "orchestrator review requested replanning"
		}
	}
	if err := runner.RejectPolishedPlan(reason); err != nil {
		if errors.Is(err, runnerpkg.ErrInvalidPhase) {
			return ErrRunnerPlanNotAwaitingReview
		}
		return err
	}
	return nil
}

// ReplanRunner replaces the runner's plan and node graph and restarts the
// polishing phase.
func (o *Orchestrator) ReplanRunner(ctx context.Context, id string, plan *CoarsePlan) error {
	runner, err := o.Runner(id)
	if err != nil {
		return err
	}
	if runner.Phase() != runnerpkg.PhaseAwaitingReplan {
		return ErrRunnerPlanNotAwaitingReplan
	}
	if plan == nil {
		o.mu.RLock()
		skills := cloneSkillMap(o.skills)
		orchestratorSkill := o.orchestratorSkill
		o.mu.RUnlock()
		envClient, envClientErr := o.resolveEnvClient()
		plan, err = replanWithOrchestratorSkill(ctx, runner.Plan().Request, runner.Plan(), runner.ReplanReason(), runner.PolishFragments(), skills, orchestratorSkill, envClient, envClientErr)
		if err != nil {
			return err
		}
	}
	nodes, err := buildPlanNodes(o, plan)
	if err != nil {
		return err
	}
	if err := runner.ReplanWith(ctx, plan, nodes); err != nil {
		if errors.Is(err, runnerpkg.ErrInvalidPhase) {
			return ErrRunnerPlanNotAwaitingReplan
		}
		return err
	}
	return nil
}

// CompleteRunner drives the identified runner through its cooperative settle
// path from executing into report and settled.
func (o *Orchestrator) CompleteRunner(ctx context.Context, id string) error {
	runner, err := o.Runner(id)
	if err != nil {
		return err
	}
	if err := runner.Complete(ctx); err != nil {
		if errors.Is(err, runnerpkg.ErrInvalidPhase) {
			return ErrRunnerNotExecuting
		}
		return err
	}
	return nil
}

func buildPlanNodes(o *Orchestrator, plan *CoarsePlan) (map[string]*runnerpkg.Node, error) {
	if plan == nil {
		return nil, fmt.Errorf("orchestrate: nil plan")
	}
	o.mu.RLock()
	logger := o.logger
	store := o.runnerStore
	skills := cloneAddSkillConfigs(o.skills)
	o.mu.RUnlock()

	request := &Request{Input: plan.Request}
	runner, err := buildRunner(logger, store, request, plan, skills)
	if err != nil {
		return nil, err
	}
	return runner.Nodes(), nil
}
