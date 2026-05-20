package engine

import (
	"context"
	"testing"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
)

func TestSubmitManagedRunner_QueuesUntilRunningRunnerSettles(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetMaxRunningRunners(1); err != nil {
		t.Fatalf("SetMaxRunningRunners: %v", err)
	}

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	first := newManagedQueueTestRunner(t, "first", func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(firstStarted)
		<-firstRelease
		return "first", nil, nil
	})
	submitQueueTestRunner(t, o, context.Background(), first, RequestPriorityDefault)
	waitForClosed(t, firstStarted, "first started")

	secondStarted := make(chan struct{})
	second := newManagedQueueTestRunner(t, "second", func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(secondStarted)
		return "second", nil, nil
	})
	submitQueueTestRunner(t, o, context.Background(), second, RequestPriorityDefault)
	assertNotClosed(t, secondStarted, 150*time.Millisecond, "second started while first still owned the slot")

	close(firstRelease)
	waitForRunnerDone(t, first, "first done")
	waitForClosed(t, secondStarted, "second started after first settled")
	waitForRunnerDone(t, second, "second done")
	waitForRunningRunnerIDs(t, o, 0)
}

func TestSubmitManagedRunner_QueuesByPriority(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetMaxRunningRunners(1); err != nil {
		t.Fatalf("SetMaxRunningRunners: %v", err)
	}

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	first := newManagedQueueTestRunner(t, "first", func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(firstStarted)
		<-firstRelease
		return "first", nil, nil
	})
	submitQueueTestRunner(t, o, context.Background(), first, RequestPriorityDefault)
	waitForClosed(t, firstStarted, "first started")

	lowStarted := make(chan struct{})
	lowRelease := make(chan struct{})
	low := newManagedQueueTestRunner(t, "low", func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(lowStarted)
		<-lowRelease
		return "low", nil, nil
	})
	submitQueueTestRunner(t, o, context.Background(), low, 1)

	highStarted := make(chan struct{})
	highRelease := make(chan struct{})
	high := newManagedQueueTestRunner(t, "high", func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(highStarted)
		<-highRelease
		return "high", nil, nil
	})
	submitQueueTestRunner(t, o, context.Background(), high, RequestPriorityHighest)

	close(firstRelease)
	waitForRunnerDone(t, first, "first done")
	waitForClosed(t, highStarted, "high priority started")
	assertNotClosed(t, lowStarted, 150*time.Millisecond, "low started before high settled")
	close(highRelease)
	waitForRunnerDone(t, high, "high done")
	waitForClosed(t, lowStarted, "low started after high settled")
	close(lowRelease)
	waitForRunnerDone(t, low, "low done")
}

func TestSubmitManagedRunner_CanceledWhileQueuedTransitionsRunner(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetMaxRunningRunners(1); err != nil {
		t.Fatalf("SetMaxRunningRunners: %v", err)
	}

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	first := newManagedQueueTestRunner(t, "first", func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(firstStarted)
		<-firstRelease
		return "first", nil, nil
	})
	submitQueueTestRunner(t, o, context.Background(), first, RequestPriorityDefault)
	waitForClosed(t, firstStarted, "first started")

	queuedStarted := make(chan struct{})
	queued := newManagedQueueTestRunner(t, "queued", func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(queuedStarted)
		return "queued", nil, nil
	})
	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	submitQueueTestRunner(t, o, queuedCtx, queued, RequestPriorityDefault)
	cancelQueued()
	waitForRunnerSettled(t, queued, "queued canceled")
	if got := queued.State().Status; got != RunnerStatusCanceled {
		t.Fatalf("queued status = %q, want canceled", got)
	}

	close(firstRelease)
	waitForRunnerDone(t, first, "first done")
	assertNotClosed(t, queuedStarted, 150*time.Millisecond, "queued runner started after cancellation")
}

func newManagedQueueTestRunner(t *testing.T, request string, run func(context.Context, map[string]any) (any, []*Node, error)) *runnerpkg.Runner {
	t.Helper()
	plan := &CoarsePlan{
		Request: request,
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: request,
		}},
	}
	nodes := map[string]*runnerpkg.Node{
		"only": {
			Id: "only",
			Agent: &stubRunnerAgent{
				polish:  func(ctx context.Context) (any, error) { return request + " polish", nil },
				execute: run,
				report:  func(ctx context.Context, output any) (any, error) { return output, nil },
			},
		},
	}
	return runnerpkg.New(
		runnerpkg.WithContext(context.Background()),
		runnerpkg.WithLogger(testLogger),
		runnerpkg.WithInitialPlan(plan, nodes),
		runnerpkg.WithPlannerSkill(bindTestOrchestratorSkill(t, mustReviewJSON(t, true, ""))),
		runnerpkg.WithSkillSummaries([]runnerpkg.SkillSummary{{Name: "single", Description: "Single test skill."}}),
		runnerpkg.WithPlanNodeBuilder(func(plan *runnerpkg.CoarsePlan) (map[string]*runnerpkg.Node, error) { return nodes, nil }),
	)
}

func submitQueueTestRunner(t *testing.T, o *Orchestrator, ctx context.Context, runner *runnerpkg.Runner, priority RequestPriority) {
	t.Helper()
	go o.watchRunnerDone(runner)
	if err := o.submitManagedRunner(ctx, runner, priority); err != nil {
		t.Fatalf("submit runner %q: %v", runner.ID(), err)
	}
}

func waitForClosed(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func assertNotClosed(t *testing.T, ch <-chan struct{}, delay time.Duration, label string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("unexpected close: %s", label)
	case <-time.After(delay):
	}
}

func waitForRunnerDone(t *testing.T, runner *runnerpkg.Runner, label string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.Wait(ctx); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
}

func waitForRunnerSettled(t *testing.T, runner *runnerpkg.Runner, label string) {
	t.Helper()
	select {
	case <-runner.Done():
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitForRunningRunnerIDs(t *testing.T, o *Orchestrator, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		o.mu.RLock()
		got := len(o.runningRunnerIDs)
		o.mu.RUnlock()
		if got == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("runningRunnerIDs size = %d, want %d", got, want)
		case <-tick.C:
		}
	}
}
