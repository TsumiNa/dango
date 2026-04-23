package orchestrate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcceptRunnerPlan_StartsExecution(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, managedRunner := mustPlanSingleNodeRunner(t, o)

	started := make(chan struct{})
	release := make(chan struct{})
	mustNodeExecutor(t, managedRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(started)
		<-release
		return "done", nil, nil
	}

	if err := o.StartRunner(context.Background(), plan.RunnerID); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(plan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner: %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.AcceptRunnerPlan(context.Background(), plan.RunnerID, plan); err != nil {
		t.Fatalf("AcceptRunnerPlan: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accepted runner to start")
	}
	close(release)
}

func TestAcceptRunnerPlan_RejectsWrongPhase(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRunner(t, o)
	if err := o.AcceptRunnerPlan(context.Background(), plan.RunnerID, plan); !errors.Is(err, ErrRunnerPlanNotAwaitingReview) {
		t.Fatalf("AcceptRunnerPlan err = %v, want ErrRunnerPlanNotAwaitingReview", err)
	}
}

func TestReviewRunnerPlan_ReturnsAutomaticDecision(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRunnerWithOutputs(t, o,
		mustPlanJSON(t, &CoarsePlan{
			Request: "run a single node",
			Nodes: []CoarsePlanNode{{
				ID:              "only",
				SkillName:       "single",
				TaskDescription: "Run the only node.",
			}},
		}),
		mustReviewJSON(t, false, "needs a clearer task breakdown"),
	)
	if err := o.StartRunner(context.Background(), plan.RunnerID); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(plan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner: %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	review, err := o.ReviewRunnerPlan(context.Background(), plan.RunnerID)
	if err != nil {
		t.Fatalf("ReviewRunnerPlan: %v", err)
	}
	if review.Approved {
		t.Fatal("ReviewRunnerPlan approved plan, want rejection")
	}
	if review.Reason != "needs a clearer task breakdown" {
		t.Fatalf("ReviewRunnerPlan reason = %q, want %q", review.Reason, "needs a clearer task breakdown")
	}
}

func TestRejectAndReplanRunner_TransitionsBackToAwaitingReview(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRunner(t, o)
	if err := o.StartRunner(context.Background(), plan.RunnerID); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(plan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner: %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.RejectRunnerPlan(context.Background(), plan.RunnerID, "needs rework"); err != nil {
		t.Fatalf("RejectRunnerPlan: %v", err)
	}
	view, err := o.QueryRunner(plan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner(after reject): %v", err)
	}
	if view.Phase != PhaseAwaitingReplan {
		t.Fatalf("phase after reject = %q, want awaiting replan", view.Phase)
	}
	replanned := &CoarsePlan{
		Request: plan.Request,
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node again.",
		}},
	}
	if err := o.ReplanRunner(context.Background(), plan.RunnerID, replanned); err != nil {
		t.Fatalf("ReplanRunner: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err = o.QueryRunner(plan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(after replan): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("runner did not return to awaiting review after replanning")
}

func TestRejectRunnerPlan_UsesAutomaticReviewWhenReasonEmpty(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRunnerWithOutputs(t, o,
		mustPlanJSON(t, &CoarsePlan{
			Request: "run a single node",
			Nodes: []CoarsePlanNode{{
				ID:              "only",
				SkillName:       "single",
				TaskDescription: "Run the only node.",
			}},
		}),
		mustReviewJSON(t, false, "add more structure before execution"),
	)
	if err := o.StartRunner(context.Background(), plan.RunnerID); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(plan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner: %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.RejectRunnerPlan(context.Background(), plan.RunnerID, ""); err != nil {
		t.Fatalf("RejectRunnerPlan(auto): %v", err)
	}
	runner, err := o.Runner(plan.RunnerID)
	if err != nil {
		t.Fatalf("Runner: %v", err)
	}
	if got := runner.ReplanReason(); got != "add more structure before execution" {
		t.Fatalf("ReplanReason() = %q, want %q", got, "add more structure before execution")
	}
}

func TestReplanRunner_GeneratesPlanWhenNilPlan(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRunnerWithOutputs(t, o,
		mustPlanJSON(t, &CoarsePlan{
			Request: "run a single node",
			Nodes: []CoarsePlanNode{{
				ID:              "only",
				SkillName:       "single",
				TaskDescription: "Run the only node.",
			}},
		}),
		mustReviewJSON(t, false, "make the task description more explicit"),
		mustPlanJSON(t, &CoarsePlan{
			Request: "run a single node",
			Nodes: []CoarsePlanNode{{
				ID:              "only",
				SkillName:       "single",
				TaskDescription: "Run the only node with the clarified task description.",
			}},
		}),
	)
	if err := o.StartRunner(context.Background(), plan.RunnerID); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(plan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner: %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.RejectRunnerPlan(context.Background(), plan.RunnerID, ""); err != nil {
		t.Fatalf("RejectRunnerPlan(auto): %v", err)
	}
	if err := o.ReplanRunner(context.Background(), plan.RunnerID, nil); err != nil {
		t.Fatalf("ReplanRunner(auto): %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(plan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(after auto replan): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			if got := view.Plan.Nodes[0].TaskDescription; got != "Run the only node with the clarified task description." {
				t.Fatalf("task description after auto replan = %q, want updated description", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("runner did not return to awaiting review after auto replanning")
}

func TestRejectRunnerPlan_RejectsWrongPhase(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRunner(t, o)
	if err := o.RejectRunnerPlan(context.Background(), plan.RunnerID, "nope"); !errors.Is(err, ErrRunnerPlanNotAwaitingReview) {
		t.Fatalf("RejectRunnerPlan err = %v, want ErrRunnerPlanNotAwaitingReview", err)
	}
}

func TestReplanRunner_RejectsWrongPhase(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRunner(t, o)
	if err := o.ReplanRunner(context.Background(), plan.RunnerID, plan); !errors.Is(err, ErrRunnerPlanNotAwaitingReplan) {
		t.Fatalf("ReplanRunner err = %v, want ErrRunnerPlanNotAwaitingReplan", err)
	}
}

func TestCompleteRunner_RejectsWrongPhase(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRunner(t, o)
	if err := o.CompleteRunner(context.Background(), plan.RunnerID); !errors.Is(err, ErrRunnerNotExecuting) {
		t.Fatalf("CompleteRunner err = %v, want ErrRunnerNotExecuting", err)
	}
}

func TestCompleteRunner_SettlesExecutingRunner(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, runner := mustPlanSingleNodeRunner(t, o)
	started := make(chan struct{})
	release := make(chan struct{})
	mustNodeExecutor(t, runner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(started)
		<-release
		return "done", nil, nil
	}
	if err := o.StartRunner(context.Background(), plan.RunnerID); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(plan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(awaiting review): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.AcceptRunnerPlan(context.Background(), plan.RunnerID, plan); err != nil {
		t.Fatalf("AcceptRunnerPlan: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not start")
	}
	close(release)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(plan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(executing): %v", err)
		}
		if view.State.Status == RunnerStatusIdle {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.CompleteRunner(context.Background(), plan.RunnerID); err != nil {
		t.Fatalf("CompleteRunner: %v", err)
	}
	view, err := o.QueryRunner(plan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner(final): %v", err)
	}
	if view.Phase != PhaseSettled {
		t.Fatalf("final phase = %q, want settled", view.Phase)
	}
}

func TestCompleteRunner_ReleasesExecutionSlot(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetMaxRunningRunners(1); err != nil {
		t.Fatalf("SetMaxRunningRunners: %v", err)
	}
	firstPlan, firstRunner := mustPlanSingleNodeRunner(t, o)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	mustNodeExecutor(t, firstRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(firstStarted)
		<-firstRelease
		return "first", nil, nil
	}
	if err := o.StartRunner(context.Background(), firstPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(first): %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(firstPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(first awaiting review): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.AcceptRunnerPlan(context.Background(), firstPlan.RunnerID, firstPlan); err != nil {
		t.Fatalf("AcceptRunnerPlan(first): %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first runner did not start")
	}
	close(firstRelease)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(firstPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(first idle): %v", err)
		}
		if view.State.Status == RunnerStatusIdle {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.CompleteRunner(context.Background(), firstPlan.RunnerID); err != nil {
		t.Fatalf("CompleteRunner(first): %v", err)
	}

	secondPlan, _, err := o.planFromRequest(context.Background(), &Request{Input: "run second node"})
	if err != nil {
		t.Fatalf("planFromRequest(second): %v", err)
	}
	if err := o.StartRunner(context.Background(), secondPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(second): %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(secondPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(second awaiting review): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.AcceptRunnerPlan(context.Background(), secondPlan.RunnerID, secondPlan); err != nil {
		t.Fatalf("AcceptRunnerPlan(second): %v", err)
	}
}
