package orchestrate

import (
	"context"
	"errors"
	"fmt"

	runnerpkg "github.com/tsumina/dango/internal/orchestrate/runner"
)

// AcceptRunnerPlan adopts the reviewed plan for the identified runner and
// starts execution.
func (o *Orchestrator) AcceptRunnerPlan(ctx context.Context, id string, plan *CoarsePlan) error {
	runner, err := o.Runner(id)
	if err != nil {
		return err
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
func (o *Orchestrator) RejectRunnerPlan(id string, reason string) error {
	runner, err := o.Runner(id)
	if err != nil {
		return err
	}
	if err := runner.RejectPolishedPlan(reason); err != nil {
		if errors.Is(err, runnerpkg.ErrInvalidPhase) {
			return ErrRunnerPlanNotAwaitingReview
		}
		return err
	}
	o.releaseRunnerExecutionSlot(runner.ID())
	return nil
}

// ReplanRunner replaces the runner's plan and node graph and restarts the
// polishing phase.
func (o *Orchestrator) ReplanRunner(ctx context.Context, id string, plan *CoarsePlan) error {
	runner, err := o.Runner(id)
	if err != nil {
		return err
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
	skills := cloneSkillMap(o.skills)
	o.mu.RUnlock()

	request := &Request{Input: plan.Request}
	runner, err := buildRunner(logger, store, request, plan, skills)
	if err != nil {
		return nil, err
	}
	return runner.Nodes(), nil
}
