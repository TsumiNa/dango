package orchestrate

import (
	"context"
	"testing"
	"time"

	"github.com/tsumina/dango/internal/llm"
)

func TestPlanFromRequest_ReturnsRejectWithoutPlanner(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, reject, err := o.planFromRequest(&Request{Input: "summarize this repository"})
	if err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if plan != nil {
		t.Fatalf("plan = %#v, want nil", plan)
	}
	if reject == nil {
		t.Fatal("expected a reject reason when no planner is configured")
	}
	if reject.Summary == "" || reject.Analysis == "" {
		t.Errorf("reject = %+v, want populated summary and analysis", reject)
	}
	if len(o.Runners()) != 0 {
		t.Fatalf("expected no runners to be created on rejection")
	}
}

func TestPlanFromRequest_BuildsRunnerFromPlan(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	perSkillClient := &llm.Client{}
	store := mustNewRunnerStore(t, t.TempDir())
	if err := o.SetRunnerStore(store); err != nil {
		t.Fatalf("SetRunnerStore: %v", err)
	}
	if err := o.RegisterSkill(writeTestSkill(t, "plan", "Draft a plan."), WithSkillClientFactory(func() (*llm.Client, error) {
		return perSkillClient, nil
	})); err != nil {
		t.Fatalf("RegisterSkill(plan): %v", err)
	}
	if err := o.RegisterSkill(writeTestSkill(t, "execute", "Execute a plan.")); err != nil {
		t.Fatalf("RegisterSkill(execute): %v", err)
	}
	orchestratorSkill := bindTestOrchestratorSkill(t, mustPlanJSON(t, &CoarsePlan{
		Request: "build a report",
		Nodes: []CoarsePlanNode{
			{ID: "draft", SkillName: "plan", TaskDescription: "Draft the execution outline."},
			{ID: "run", SkillName: "execute", TaskDescription: "Execute the approved outline.", DependsOn: []string{"draft"}},
		},
	}))
	if err := o.SetOrchestratorSkill(orchestratorSkill); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	plan, reject, err := o.planFromRequest(&Request{Input: "build a report"})
	if err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if reject != nil {
		t.Fatalf("reject = %+v, want nil", reject)
	}
	if plan == nil {
		t.Fatal("expected a coarse plan")
	}
	if plan.RunnerID == "" {
		t.Fatal("expected coarse plan to be annotated with a runner ID")
	}

	managedRunner := o.Runners()[plan.RunnerID]
	if managedRunner == nil {
		t.Fatalf("expected runner %q to be stored", plan.RunnerID)
	}
	if managedRunner.ID() != plan.RunnerID {
		t.Errorf("Runner.ID() = %q, want %q", managedRunner.ID(), plan.RunnerID)
	}
	if len(managedRunner.Nodes()) != 2 {
		t.Fatalf("len(Nodes) = %d, want 2", len(managedRunner.Nodes()))
	}

	draft := managedRunner.Nodes()["draft"]
	run := managedRunner.Nodes()["run"]
	if draft == nil || run == nil {
		t.Fatalf("expected draft and run nodes to exist, got draft=%v run=%v", draft, run)
	}
	draftExecutor := mustNodeExecutor(t, draft)
	if draftExecutor.Skill().Name != "plan" {
		t.Fatalf("draft executor skill = %v, want plan", draftExecutor)
	}
	if got := draftExecutor.LLMClient(); got != perSkillClient {
		t.Fatalf("draft executor LLMClient() = %p, want %p", got, perSkillClient)
	}
	runExecutor := mustNodeExecutor(t, run)
	if runExecutor.Skill().Name != "execute" {
		t.Fatalf("run executor skill = %v, want execute", runExecutor)
	}
	if got := runExecutor.LLMClient(); got != orchestratorSkill.Client() {
		t.Fatalf("run executor LLMClient() = %p, want %p", got, orchestratorSkill.Client())
	}
	if len(run.Parents) != 1 || run.Parents[0].Id != "draft" {
		t.Fatalf("run parents = %+v, want [draft]", run.Parents)
	}
	if got := runExecutor.Planner().TaskDescription; got != "Execute the approved outline." {
		t.Errorf("run task description = %q, want %q", got, "Execute the approved outline.")
	}
}

func TestPlanFromRequest_ErrorsWhenPlanUsesUnknownSkill(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, mustPlanJSON(t, &CoarsePlan{
		Request: "process images",
		Nodes:   []CoarsePlanNode{{ID: "only", SkillName: "missing", TaskDescription: "process images"}},
	}))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	plan, reject, err := o.planFromRequest(&Request{Input: "process images"})
	if err == nil {
		t.Fatal("expected error when the plan references an unknown skill")
	}
	if plan != nil || reject != nil {
		t.Fatalf("expected nil plan and reject on internal plan error, got plan=%v reject=%v", plan, reject)
	}
	if len(o.Runners()) != 0 {
		t.Fatalf("expected no runners to be stored when plan assembly fails")
	}
}

func TestStartRequest_StartsRunnerImmediately(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.RegisterSkill(writeTestSkill(t, "single", "Single-step runner.")); err != nil {
		t.Fatalf("RegisterSkill(single): %v", err)
	}
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, mustPlanJSON(t, &CoarsePlan{
		Request: "run now",
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node.",
		}},
	}))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	plan, reject, err := o.StartRequest(ctx, &Request{Input: "run now"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	if reject != nil {
		t.Fatalf("reject = %+v, want nil", reject)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, queryErr := o.QueryRunner(plan.RunnerID)
		if queryErr != nil {
			t.Fatalf("QueryRunner: %v", queryErr)
		}
		if view.Phase == PhaseAwaitingReview {
			if err := o.AcceptRunnerPlan(ctx, plan.RunnerID, plan); err != nil {
				t.Fatalf("AcceptRunnerPlan: %v", err)
			}
		}
		if view.State.Status == RunnerStatusRunning || view.State.Status == RunnerStatusIdle {
			cancel()
			finalDeadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(finalDeadline) {
				finalView, finalErr := o.QueryRunner(plan.RunnerID)
				if finalErr != nil {
					t.Fatalf("QueryRunner(final): %v", finalErr)
				}
				if finalView.State.Status == RunnerStatusCanceled {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("runner did not reach canceled after StartRequest context cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("runner did not start after StartRequest")
}

func TestStartRequest_EntersAwaitingReviewBeforeAccept(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRunner(t, o)
	if err := o.StartRunner(context.Background(), plan.RunnerID); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, queryErr := o.QueryRunner(plan.RunnerID)
		if queryErr != nil {
			t.Fatalf("QueryRunner: %v", queryErr)
		}
		if view.Phase == PhaseAwaitingReview {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("runner did not reach awaiting review")
}

func TestStartRequest_RejectsPriorityOutsideRange(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.RegisterSkill(writeTestSkill(t, "single", "Single-step runner.")); err != nil {
		t.Fatalf("RegisterSkill(single): %v", err)
	}
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, mustPlanJSON(t, &CoarsePlan{
		Request: "run now",
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node.",
		}},
	}))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	for _, priority := range []RequestPriority{-1, RequestPriorityHighest + 1} {
		plan, reject, err := o.StartRequest(context.Background(), &Request{Input: "run now", Priority: priority})
		if err == nil {
			t.Fatalf("expected StartRequest to reject priority %d", priority)
		}
		if plan != nil || reject != nil {
			t.Fatalf("expected nil plan and reject for invalid priority %d, got plan=%v reject=%v", priority, plan, reject)
		}
		if len(o.Runners()) != 0 {
			t.Fatalf("expected no runners to be created for invalid priority %d", priority)
		}
	}
}
