package runner

import (
	"context"
	"fmt"
	"sync"
)

// Complete signals that the engine has reached its end successfully,
// cancels the engine loop, runs the [PhaseReport] stage for every
// completed node, and settles the runner with a terminal [RunnerStatusIdle].
//
// Complete is the happy-path counterpart to [Runner.Abort]: the engine
// error is treated as benign, report summaries are gathered, and
// [Runner.Wait] returns nil (or the first report error). Complete is
// valid only while the runner is in [PhaseExecuting]; calls from other
// phases return [ErrInvalidPhase].
func (r *Runner) Complete(ctx context.Context) error {
	ctx = r.runtimeContext(ctx)
	r.stateMu.RLock()
	phase := r.phase
	cancel := r.cancelEngine
	r.stateMu.RUnlock()
	if phase != PhaseExecuting {
		return ErrInvalidPhase
	}
	r.completedCleanly.Store(true)
	if cancel != nil {
		cancel()
	}
	return r.Wait(ctx)
}

// StartPolish enters [PhasePolishing] and fans [Executor.Polish] across
// the initial node graph concurrently.
//
// StartPolish returns immediately once the polish stage has been launched
// in the background. When every Polish call returns, the runner transitions
// to [PhaseAwaitingReview] and publishes an update; if any Polish call
// fails the runner aborts with that error and settles.
//
// StartPolish is valid from [PhaseCreated] (first entry) and from
// [PhaseAwaitingReplan] (re-entry after a rejected plan). It requires a
// plan supplied via [WithPlan].
func (r *Runner) StartPolish(ctx context.Context) error {
	ctx = r.runtimeContext(ctx)
	if err := r.prepareNodeExecutors(r.initialNodes); err != nil {
		return err
	}
	r.stateMu.Lock()
	if r.plan == nil {
		r.stateMu.Unlock()
		return ErrPlanRequired
	}
	if r.phase != PhaseCreated && r.phase != PhaseAwaitingReplan {
		r.stateMu.Unlock()
		return ErrInvalidPhase
	}
	nodes := cloneNodeMap(r.initialNodes)
	r.phase = PhasePolishing
	r.stateMu.Unlock()

	r.publishUpdate(nil)
	go r.runPolishStage(ctx, nodes)
	return nil
}

// AcceptPolishedPlan resolves [PhaseAwaitingReview] by adopting the
// reviewed plan and launching the execution engine.
//
// It is the orchestrator's acceptance gate following [Runner.StartPolish].
// The supplied plan replaces the runner's current plan before execution
// begins. Returns [ErrInvalidPhase] if the runner is not in
// [PhaseAwaitingReview].
func (r *Runner) AcceptPolishedPlan(ctx context.Context, plan *CoarsePlan) error {
	if plan == nil {
		return ErrPlanRequired
	}
	clonedPlan := CloneCoarsePlan(plan)
	if clonedPlan.RunnerID == "" {
		clonedPlan.RunnerID = r.id
	}
	initialNodes, err := r.prepareEngineLaunch(PhaseAwaitingReview, func() {
		r.plan = clonedPlan
	})
	if err != nil {
		return err
	}
	return r.launchEngine(ctx, initialNodes)
}

// RejectPolishedPlan resolves [PhaseAwaitingReview] by recording a
// rejection reason and transitioning to [PhaseAwaitingReplan].
//
// The orchestrator is expected to observe the transition and call
// [Runner.ReplanWith] to supply a new plan, or [Runner.Abort] to settle
// the runner without replanning.
func (r *Runner) RejectPolishedPlan(reason string) error {
	r.stateMu.Lock()
	if r.phase != PhaseAwaitingReview {
		r.stateMu.Unlock()
		return ErrInvalidPhase
	}
	r.phase = PhaseAwaitingReplan
	r.stateMu.Unlock()

	r.lifecycleMu.Lock()
	r.replanReason = reason
	r.lifecycleMu.Unlock()

	r.publishUpdate(nil)
	return nil
}

// ReplanWith replaces the runner's plan and initial node graph and
// re-enters [PhasePolishing].
//
// ReplanWith is valid only from [PhaseAwaitingReplan]. Callers typically
// drive it after observing a [RejectPolishedPlan] transition and producing
// a revised plan.
func (r *Runner) ReplanWith(ctx context.Context, plan *CoarsePlan, nodes map[string]*Node) error {
	ctx = r.runtimeContext(ctx)
	if plan == nil {
		return ErrPlanRequired
	}
	clonedPlan := CloneCoarsePlan(plan)
	if clonedPlan.RunnerID == "" {
		clonedPlan.RunnerID = r.id
	}
	clonedNodes := cloneNodeMap(nodes)
	if err := r.prepareNodeExecutors(clonedNodes); err != nil {
		return err
	}

	r.stateMu.Lock()
	if r.phase != PhaseAwaitingReplan {
		r.stateMu.Unlock()
		return ErrInvalidPhase
	}
	r.plan = clonedPlan
	r.initialNodes = clonedNodes
	r.phase = PhasePolishing
	r.updateMu.Lock()
	r.snapshot = buildInitialRunnerSnapshot(clonedNodes)
	r.updateMu.Unlock()
	r.stateMu.Unlock()

	r.lifecycleMu.Lock()
	r.polishFragments = nil
	r.polishErr = nil
	r.reportSummaries = nil
	r.reportErr = nil
	r.replanReason = ""
	r.lifecycleMu.Unlock()

	r.publishUpdate(nil)
	go r.runPolishStage(ctx, cloneNodeMap(clonedNodes))
	return nil
}

// StartManaged launches the full runner-owned lifecycle in the background.
//
// The runner polishes its plan, reviews the polished fragments with the same
// planner skill used to create the original plan, replans when review rejects
// the candidate, executes the accepted graph, and finally runs report before
// settling. StartManaged is non-blocking; callers observe progress through
// [Runner.View], [Runner.SubscribeUpdates], and [Runner.Wait].
func (r *Runner) StartManaged(ctx context.Context) error {
	ctx = r.runtimeContext(ctx)
	if err := ctx.Err(); err != nil {
		r.Abort(err)
		return err
	}

	started := false
	r.managedStartOnce.Do(func() {
		started = true
		r.stateMu.RLock()
		hasPlan := r.plan != nil
		phase := r.phase
		r.stateMu.RUnlock()
		if !hasPlan {
			r.managedStartErr = ErrPlanRequired
			return
		}
		if phase != PhaseCreated {
			r.managedStartErr = ErrInvalidPhase
			return
		}
		go r.runManagedLifecycle(ctx)
	})
	if !started {
		return ErrRunnerAlreadyStarted
	}
	return r.managedStartErr
}

func (r *Runner) runManagedLifecycle(ctx context.Context) {
	if err := r.runManagedLifecycleE(ctx); err != nil {
		select {
		case <-r.Done():
		default:
			r.Abort(err)
		}
	}
}

func (r *Runner) runManagedLifecycleE(ctx context.Context) error {
	if err := r.StartPolish(ctx); err != nil {
		return err
	}
	if err := r.waitForPhase(ctx, PhaseAwaitingReview); err != nil {
		return err
	}
	for {
		review, err := r.reviewPolishedPlan(ctx)
		if err != nil {
			return err
		}
		if review.Approved {
			return r.acceptAndComplete(ctx)
		}
		reason := review.Reason
		if reason == "" {
			reason = "planner review requested replanning"
		}
		if err := r.RejectPolishedPlan(reason); err != nil {
			return err
		}
		plan, nodes, err := r.replanPolishedPlan(ctx, reason)
		if err != nil {
			return err
		}
		if err := r.ReplanWith(ctx, plan, nodes); err != nil {
			return err
		}
		if err := r.waitForPhase(ctx, PhaseAwaitingReview); err != nil {
			return err
		}
	}
}

func (r *Runner) acceptAndComplete(ctx context.Context) error {
	events := r.Subscribe(32)
	if err := r.AcceptPolishedPlan(ctx, r.Plan()); err != nil {
		return err
	}
	for {
		select {
		case event := <-events:
			if event.Type == EventEngineIdle {
				return r.Complete(ctx)
			}
			if event.Type == EventNodeFailed || event.Type == EventEngineStopped {
				return r.Wait(ctx)
			}
		case <-r.Done():
			return r.Wait(context.Background())
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *Runner) waitForPhase(ctx context.Context, want RunnerPhase) error {
	if r.Phase() == want {
		return nil
	}
	updates, unsubscribe := r.SubscribeUpdates(16)
	defer unsubscribe()
	for {
		select {
		case update, ok := <-updates:
			if !ok {
				return r.Wait(context.Background())
			}
			if update.Phase == want {
				return nil
			}
			if update.Phase == PhaseSettled {
				return r.Wait(context.Background())
			}
		case <-r.Done():
			return r.Wait(context.Background())
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *Runner) runPolishStage(ctx context.Context, nodes map[string]*Node) {
	fragments, err := fanOutPolish(ctx, nodes)

	r.lifecycleMu.Lock()
	r.polishFragments = fragments
	r.polishErr = err
	r.lifecycleMu.Unlock()

	if err != nil {
		r.Abort(err)
		return
	}

	r.transitionPhase(PhaseAwaitingReview)
	r.publishUpdate(nil)
}

func (r *Runner) runReportStage(ctx context.Context) {
	ctx = r.runtimeContext(ctx)
	r.transitionPhase(PhaseReport)
	r.publishUpdate(nil)

	r.updateMu.Lock()
	snapshot := cloneRunnerSnapshot(r.snapshot)
	r.updateMu.Unlock()

	summaries, err := fanOutReport(ctx, snapshot.NodesData, snapshot.CompletedNodes)

	r.lifecycleMu.Lock()
	r.reportSummaries = summaries
	r.reportErr = err
	r.lifecycleMu.Unlock()

	if err != nil {
		r.captureEngineErr(err)
	}
}

func fanOutPolish(ctx context.Context, nodes map[string]*Node) (map[string]any, error) {
	if len(nodes) == 0 {
		return map[string]any{}, nil
	}
	fragments := make(map[string]any, len(nodes))
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	for id, node := range nodes {
		if node == nil || node.Executor == nil {
			continue
		}
		wg.Add(1)
		go func(id string, executor Executor) {
			defer wg.Done()
			frag, err := executor.Polish(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("polish %s: %w", id, err)
				}
				return
			}
			fragments[id] = frag
		}(id, node.Executor)
	}
	wg.Wait()
	if firstErr != nil {
		return fragments, firstErr
	}
	return fragments, nil
}

func fanOutReport(ctx context.Context, nodes map[string]*Node, outputs map[string]any) (map[string]any, error) {
	if len(outputs) == 0 {
		return map[string]any{}, nil
	}
	summaries := make(map[string]any, len(outputs))
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	for id, output := range outputs {
		node := nodes[id]
		if node == nil || node.Executor == nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("report %s: missing executor", id)
			}
			continue
		}
		wg.Add(1)
		go func(id string, executor Executor, output any) {
			defer wg.Done()
			summary, err := executor.Report(ctx, output)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("report %s: %w", id, err)
				}
				return
			}
			summaries[id] = summary
		}(id, node.Executor, output)
	}
	wg.Wait()
	if firstErr != nil {
		return summaries, firstErr
	}
	return summaries, nil
}

// PolishFragments returns the per-node fragments produced during
// [PhasePolishing].
//
// The returned map is a shallow copy safe for the caller to retain. Nodes
// whose Polish call returned a nil fragment are still present as nil.
func (r *Runner) PolishFragments() map[string]any {
	r.lifecycleMu.RLock()
	defer r.lifecycleMu.RUnlock()
	if r.polishFragments == nil {
		return nil
	}
	out := make(map[string]any, len(r.polishFragments))
	for k, v := range r.polishFragments {
		out[k] = v
	}
	return out
}

// ReportSummaries returns the per-node summaries produced during
// [PhaseReport].
//
// The returned map is a shallow copy safe for the caller to retain.
func (r *Runner) ReportSummaries() map[string]any {
	r.lifecycleMu.RLock()
	defer r.lifecycleMu.RUnlock()
	if r.reportSummaries == nil {
		return nil
	}
	out := make(map[string]any, len(r.reportSummaries))
	for k, v := range r.reportSummaries {
		out[k] = v
	}
	return out
}

// ReplanReason returns the rejection reason recorded by
// [Runner.RejectPolishedPlan], or the empty string if no rejection has
// occurred.
func (r *Runner) ReplanReason() string {
	r.lifecycleMu.RLock()
	defer r.lifecycleMu.RUnlock()
	return r.replanReason
}
