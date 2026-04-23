package orchestrate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tsumina/dango/internal/llm/skill"
	runnerpkg "github.com/tsumina/dango/internal/orchestrate/runner"
)

func resetDefaultOrchestrator(t *testing.T) {
	t.Helper()
	defaultOrchestrator = nil
	defaultOrchestratorOnce = sync.Once{}
}

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeTestSkill(t *testing.T, name, description string) string {
	t.Helper()
	dir := t.TempDir()
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\nbody"
	if err := os.WriteFile(filepath.Join(dir, skill.SkillFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return dir
}

func mustPlanSingleNodeRunner(t *testing.T, o *Orchestrator) (*CoarsePlan, *runnerpkg.Runner) {
	t.Helper()
	if err := o.RegisterSkill(writeTestSkill(t, "single", "Single-step runner.")); err != nil {
		t.Fatalf("RegisterSkill(single): %v", err)
	}
	if err := o.SetPlanningFunc(func(req *Request, skills map[string]*skill.Skill) (*CoarsePlan, *RejectReason, error) {
		return &CoarsePlan{
			Request: req.Input,
			Nodes: []CoarsePlanNode{{
				ID:              "only",
				SkillName:       "single",
				TaskDescription: "Run the only node.",
			}},
		}, nil, nil
	}); err != nil {
		t.Fatalf("SetPlanningFunc: %v", err)
	}
	plan, reject, err := o.planFromRequest(&Request{Input: "run a single node"})
	if err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if reject != nil {
		t.Fatalf("reject = %+v, want nil", reject)
	}
	managedRunner, ok := o.Runners()[plan.RunnerID]
	if !ok || managedRunner == nil {
		t.Fatalf("expected runner %q to be stored", plan.RunnerID)
	}
	return plan, managedRunner
}

func waitForRunnerUpdate(t *testing.T, ch <-chan RunnerUpdate, predicate func(RunnerUpdate) bool, label string) RunnerUpdate {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatalf("timed out waiting for runner update: %s", label)
		case update, ok := <-ch:
			if !ok {
				t.Fatalf("runner update stream closed while waiting for %s", label)
			}
			if predicate(update) {
				return update
			}
		}
	}
}

func waitForRunnerUpdateClosed(t *testing.T, ch <-chan RunnerUpdate, label string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("runner update stream still open while waiting for %s", label)
		}
	case <-timer.C:
		t.Fatalf("timed out waiting for runner update stream to close: %s", label)
	}
}

func TestDefault_ReturnsSingleton(t *testing.T) {
	resetDefaultOrchestrator(t)
	o1 := Default()
	o2 := Default()
	if o1 != o2 {
		t.Fatalf("Default() should return the singleton instance")
	}
	if o1.logger != slog.Default() {
		t.Fatalf("logger = %p, want %p", o1.logger, slog.Default())
	}
}

func TestSetLogger_ReconfiguresSingletonLogger(t *testing.T) {
	resetDefaultOrchestrator(t)
	second := slog.New(slog.NewJSONHandler(io.Discard, nil))

	o := Default()
	if got := o.logger; got != slog.Default() {
		t.Fatalf("initial logger = %p, want %p", got, slog.Default())
	}

	if err := o.SetLogger(second); err != nil {
		t.Fatalf("SetLogger: %v", err)
	}
	if got := Default(); got != o {
		t.Fatalf("Default() returned %p, want %p", got, o)
	}
	if got := o.logger; got != second {
		t.Fatalf("reconfigured logger = %p, want %p", got, second)
	}
}

func TestSetLogger_NilRestoresDefaultLogger(t *testing.T) {
	resetDefaultOrchestrator(t)
	o := Default()
	if err := o.SetLogger(newDiscardLogger()); err != nil {
		t.Fatalf("SetLogger(custom): %v", err)
	}
	if err := o.SetLogger(nil); err != nil {
		t.Fatalf("SetLogger(nil): %v", err)
	}
	if got := o.logger; got != slog.Default() {
		t.Fatalf("logger after nil reset = %p, want %p", got, slog.Default())
	}
}

func TestSetLogger_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.planFromRequest(&Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if err := o.SetLogger(newDiscardLogger()); err == nil {
		t.Fatal("expected SetLogger to fail after startup")
	}
	if got := o.logger; got != testLogger {
		t.Fatalf("logger = %p, want %p", got, testLogger)
	}
}

func TestSetRunnerStore_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.planFromRequest(&Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if err := o.SetRunnerStore(mustNewRunnerStore(t, t.TempDir())); err == nil {
		t.Fatal("expected SetRunnerStore to fail after startup")
	}
}

func TestSetMaxRunningRunners_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.planFromRequest(&Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if err := o.SetMaxRunningRunners(1); err == nil {
		t.Fatal("expected SetMaxRunningRunners to fail after startup")
	}
}

func TestRunner_ReturnsManagedRunnerByID(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, managedRunner := mustPlanSingleNodeRunner(t, o)

	got, err := o.Runner(plan.RunnerID)
	if err != nil {
		t.Fatalf("Runner: %v", err)
	}
	if got != managedRunner {
		t.Fatalf("Runner() = %p, want %p", got, managedRunner)
	}
}

func TestRunner_RejectsUnknownID(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, err := o.Runner("missing"); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("Runner err = %v, want ErrRunnerNotFound", err)
	}
}

func TestQueryRunner_ReturnsRunnerView(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRunner(t, o)

	view, err := o.QueryRunner(plan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner: %v", err)
	}
	if view.RunnerID != plan.RunnerID {
		t.Fatalf("RunnerID = %q, want %q", view.RunnerID, plan.RunnerID)
	}
	if view.Plan == nil || view.Plan.Request != plan.Request {
		t.Fatalf("Plan = %+v, want request %q", view.Plan, plan.Request)
	}
	if view.State.Status != RunnerStatusPending {
		t.Fatalf("state = %q, want pending", view.State.Status)
	}
	if _, ok := view.Snapshot.NodesData["only"]; !ok {
		t.Fatal("expected query snapshot to include the only node")
	}
}

func TestSubscribeRunner_RejectsUnknownID(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.SubscribeRunner("missing", 4); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("SubscribeRunner err = %v, want ErrRunnerNotFound", err)
	}
}

func TestRegisterSkill_LoadsLightweightSkill(t *testing.T) {
	o := newOrchestrator(testLogger)
	dir := writeTestSkill(t, "test-skill", "A skill for orchestrator test.")

	if err := o.RegisterSkill(dir); err != nil {
		t.Fatalf("RegisterSkill: %v", err)
	}

	sk := o.Skills()["test-skill"]
	if sk == nil {
		t.Fatalf("expected test-skill to be registered")
	}
	if sk.Name != "test-skill" {
		t.Errorf("Name = %q, want %q", sk.Name, "test-skill")
	}
	if sk.Client() != nil {
		t.Errorf("Client() = %p, want nil", sk.Client())
	}
	if sk.Conversation() != nil {
		t.Errorf("Conversation() should be nil for a lightweight registered skill")
	}
	if sk.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", sk.Dir(), dir)
	}
}

func TestRegisterSkill_RejectsDuplicateSkillNames(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.RegisterSkill(writeTestSkill(t, "duplicate", "first")); err != nil {
		t.Fatalf("RegisterSkill(first): %v", err)
	}
	if err := o.RegisterSkill(writeTestSkill(t, "duplicate", "second")); err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
}

func TestRegisterSkill_AllowsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.planFromRequest(&Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}

	dir := writeTestSkill(t, "late-skill", "Registered after startup.")
	if err := o.RegisterSkill(dir); err != nil {
		t.Fatalf("RegisterSkill: %v", err)
	}

	sk := o.Skills()["late-skill"]
	if sk == nil {
		t.Fatal("expected late-skill to be registered after startup")
	}
	if sk.Dir() != dir {
		t.Fatalf("Dir() = %q, want %q", sk.Dir(), dir)
	}
}

func TestRemoveSkill_AllowsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.RegisterSkill(writeTestSkill(t, "ephemeral", "Removed after startup.")); err != nil {
		t.Fatalf("RegisterSkill: %v", err)
	}
	if _, _, err := o.planFromRequest(&Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}

	if err := o.RemoveSkill("ephemeral"); err != nil {
		t.Fatalf("RemoveSkill: %v", err)
	}
	if sk := o.Skills()["ephemeral"]; sk != nil {
		t.Fatal("expected ephemeral to be removed after startup")
	}
}

func TestRemoveSkill_RejectsUnknownSkill(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.RemoveSkill("missing"); err == nil {
		t.Fatal("expected RemoveSkill to fail for an unknown skill")
	}
}

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
	o := newOrchestrator(testLogger)
	store := mustNewRunnerStore(t, t.TempDir())
	if err := o.SetRunnerStore(store); err != nil {
		t.Fatalf("SetRunnerStore: %v", err)
	}
	if err := o.RegisterSkill(writeTestSkill(t, "plan", "Draft a plan.")); err != nil {
		t.Fatalf("RegisterSkill(plan): %v", err)
	}
	if err := o.RegisterSkill(writeTestSkill(t, "execute", "Execute a plan.")); err != nil {
		t.Fatalf("RegisterSkill(execute): %v", err)
	}

	if err := o.SetPlanningFunc(func(req *Request, skills map[string]*skill.Skill) (*CoarsePlan, *RejectReason, error) {
		if req.Input != "build a report" {
			t.Fatalf("unexpected request input %q", req.Input)
		}
		if len(skills) != 2 {
			t.Fatalf("len(skills) = %d, want 2", len(skills))
		}
		return &CoarsePlan{
			Request: req.Input,
			Nodes: []CoarsePlanNode{
				{ID: "draft", SkillName: "plan", TaskDescription: "Draft the execution outline."},
				{ID: "run", SkillName: "execute", TaskDescription: "Execute the approved outline.", DependsOn: []string{"draft"}},
			},
		}, nil, nil
	}); err != nil {
		t.Fatalf("SetPlanningFunc: %v", err)
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
	runExecutor := mustNodeExecutor(t, run)
	if runExecutor.Skill().Name != "execute" {
		t.Fatalf("run executor skill = %v, want execute", runExecutor)
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
	if err := o.SetPlanningFunc(func(req *Request, skills map[string]*skill.Skill) (*CoarsePlan, *RejectReason, error) {
		return &CoarsePlan{
			Request: req.Input,
			Nodes:   []CoarsePlanNode{{ID: "only", SkillName: "missing", TaskDescription: req.Input}},
		}, nil, nil
	}); err != nil {
		t.Fatalf("SetPlanningFunc: %v", err)
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

func TestLoadRunnerRecords_RequiresConfiguredStore(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRunner(t, o)

	_, err := o.LoadRunnerRecords(context.Background(), plan.RunnerID)
	if !errors.Is(err, ErrRunnerStoreNotConfigured) {
		t.Fatalf("LoadRunnerRecords err = %v, want ErrRunnerStoreNotConfigured", err)
	}
}

func TestStartRunner_ForwardsStreamAndQueryState(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, managedRunner := mustPlanSingleNodeRunner(t, o)

	started := make(chan struct{})
	release := make(chan struct{})
	mustNodeExecutor(t, managedRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(started)
		<-release
		return "done", nil, nil
	}

	updates, unsubscribe, err := o.SubscribeRunner(plan.RunnerID, 8)
	if err != nil {
		t.Fatalf("SubscribeRunner: %v", err)
	}
	defer unsubscribe()

	initial := waitForRunnerUpdate(t, updates, func(update RunnerUpdate) bool {
		return update.Event == nil
	}, "initial update")
	if initial.State.Status != RunnerStatusPending {
		t.Fatalf("initial state = %q, want pending", initial.State.Status)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := o.StartRunner(ctx, plan.RunnerID); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	awaitingReviewUpdate := waitForRunnerUpdate(t, updates, func(update RunnerUpdate) bool {
		return update.Phase == runnerpkg.PhaseAwaitingReview
	}, "awaiting review update")
	if awaitingReviewUpdate.State.Status != RunnerStatusPending {
		t.Fatalf("state while awaiting review = %q, want pending", awaitingReviewUpdate.State.Status)
	}
	if err := o.AcceptRunnerPlan(ctx, plan.RunnerID, plan); err != nil {
		t.Fatalf("AcceptRunnerPlan: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runner to start after plan acceptance")
	}

	startedUpdate := waitForRunnerUpdate(t, updates, func(update RunnerUpdate) bool {
		return update.Event != nil && update.Event.Type == EventNodeStarted
	}, "node started")
	if startedUpdate.State.Status != RunnerStatusRunning {
		t.Fatalf("state after node started = %q, want running", startedUpdate.State.Status)
	}

	view, err := o.QueryRunner(plan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner: %v", err)
	}
	if view.State.Status != RunnerStatusRunning {
		t.Fatalf("queried state = %q, want running", view.State.Status)
	}

	close(release)
	completedUpdate := waitForRunnerUpdate(t, updates, func(update RunnerUpdate) bool {
		return update.Event != nil && update.Event.Type == EventNodeCompleted
	}, "node completed")
	if completedUpdate.Event.NodeID != "only" {
		t.Fatalf("completed node = %q, want only", completedUpdate.Event.NodeID)
	}
	idleUpdate := waitForRunnerUpdate(t, updates, func(update RunnerUpdate) bool {
		return update.Event != nil && update.Event.Type == EventEngineIdle
	}, "engine idle")
	if idleUpdate.State.Status != RunnerStatusIdle {
		t.Fatalf("idle state = %q, want idle", idleUpdate.State.Status)
	}

	cancel()
	terminalUpdate := waitForRunnerUpdate(t, updates, func(update RunnerUpdate) bool {
		return update.State.Status == RunnerStatusCanceled
	}, "canceled terminal update")
	if terminalUpdate.State.Status != RunnerStatusCanceled {
		t.Fatalf("terminal state = %q, want canceled", terminalUpdate.State.Status)
	}
	finalView, err := o.QueryRunner(plan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner(final): %v", err)
	}
	if finalView.State.Status != RunnerStatusCanceled {
		t.Fatalf("final queried state = %q, want canceled", finalView.State.Status)
	}
	waitForRunnerUpdateClosed(t, updates, "canceled terminal update")
}

func TestStartRequest_StartsRunnerImmediately(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.RegisterSkill(writeTestSkill(t, "single", "Single-step runner.")); err != nil {
		t.Fatalf("RegisterSkill(single): %v", err)
	}
	if err := o.SetPlanningFunc(func(req *Request, skills map[string]*skill.Skill) (*CoarsePlan, *RejectReason, error) {
		return &CoarsePlan{
			Request: req.Input,
			Nodes: []CoarsePlanNode{{
				ID:              "only",
				SkillName:       "single",
				TaskDescription: "Run the only node.",
			}},
		}, nil, nil
	}); err != nil {
		t.Fatalf("SetPlanningFunc: %v", err)
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
		if view.Phase == runnerpkg.PhaseAwaitingReview {
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
		if view.Phase == runnerpkg.PhaseAwaitingReview {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("runner did not reach awaiting review")
}

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
		if view.Phase == runnerpkg.PhaseAwaitingReview {
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
		if view.Phase == runnerpkg.PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.RejectRunnerPlan(plan.RunnerID, "needs rework"); err != nil {
		t.Fatalf("RejectRunnerPlan: %v", err)
	}
	view, err := o.QueryRunner(plan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner(after reject): %v", err)
	}
	if view.Phase != runnerpkg.PhaseAwaitingReplan {
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
		if view.Phase == runnerpkg.PhaseAwaitingReview {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("runner did not return to awaiting review after replanning")
}

func TestRejectRunnerPlan_RejectsWrongPhase(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRunner(t, o)
	if err := o.RejectRunnerPlan(plan.RunnerID, "nope"); !errors.Is(err, ErrRunnerPlanNotAwaitingReview) {
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

func TestQueuedRunnerCanReachAwaitingReviewWithoutConsumingExecutionSlot(t *testing.T) {
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
			t.Fatalf("QueryRunner(first): %v", err)
		}
		if view.Phase == runnerpkg.PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	secondPlan, _, err := o.planFromRequest(&Request{Input: "run second node"})
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
			t.Fatalf("QueryRunner(second): %v", err)
		}
		if view.Phase == runnerpkg.PhaseAwaitingReview {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("second runner did not reach awaiting review while no runner was executing")
}

func TestAcceptRunnerPlan_RespectsExecutionSlotLimit(t *testing.T) {
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
			t.Fatalf("QueryRunner(first): %v", err)
		}
		if view.Phase == runnerpkg.PhaseAwaitingReview {
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
		t.Fatal("first runner did not start executing")
	}
	if len(o.runningRunnerIDs) != 1 {
		t.Fatalf("runningRunnerIDs size = %d, want 1 while first runner is executing", len(o.runningRunnerIDs))
	}

	secondPlan, _, err := o.planFromRequest(&Request{Input: "run second node"})
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
			t.Fatalf("QueryRunner(second): %v", err)
		}
		if view.Phase == runnerpkg.PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.AcceptRunnerPlan(context.Background(), secondPlan.RunnerID, secondPlan); !errors.Is(err, ErrRunnerExecutionSlotsFull) {
		t.Fatalf("AcceptRunnerPlan(second) err = %v, want ErrRunnerExecutionSlotsFull", err)
	}
	close(firstRelease)
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
		if view.Phase == runnerpkg.PhaseAwaitingReview {
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
	if view.Phase != runnerpkg.PhaseSettled {
		t.Fatalf("final phase = %q, want settled", view.Phase)
	}
}

func TestStartRequest_RejectsPriorityOutsideRange(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.RegisterSkill(writeTestSkill(t, "single", "Single-step runner.")); err != nil {
		t.Fatalf("RegisterSkill(single): %v", err)
	}
	if err := o.SetPlanningFunc(func(req *Request, skills map[string]*skill.Skill) (*CoarsePlan, *RejectReason, error) {
		return &CoarsePlan{
			Request: req.Input,
			Nodes: []CoarsePlanNode{{
				ID:              "only",
				SkillName:       "single",
				TaskDescription: "Run the only node.",
			}},
		}, nil, nil
	}); err != nil {
		t.Fatalf("SetPlanningFunc: %v", err)
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

func TestStartRequest_QueuesByPriorityWhenLimitReached(t *testing.T) {
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
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	if err := o.StartRunner(firstCtx, firstPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(first): %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(firstPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(first awaiting review): %v", err)
		}
		if view.Phase == runnerpkg.PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if view, err := o.QueryRunner(firstPlan.RunnerID); err != nil {
		t.Fatalf("QueryRunner(first before accept): %v", err)
	} else if view.Phase != runnerpkg.PhaseAwaitingReview {
		t.Fatalf("first runner phase before accept = %q, want awaiting review", view.Phase)
	}
	if err := o.AcceptRunnerPlan(firstCtx, firstPlan.RunnerID, firstPlan); err != nil {
		t.Fatalf("AcceptRunnerPlan(first): %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first runner to start")
	}

	lowCtx, cancelLow := context.WithCancel(context.Background())
	defer cancelLow()
	lowPlan, reject, err := o.StartRequest(lowCtx, &Request{Input: "low", Priority: 1})
	if err != nil {
		t.Fatalf("StartRequest(low): %v", err)
	}
	if reject != nil {
		t.Fatalf("reject(low) = %+v, want nil", reject)
	}

	highCtx, cancelHigh := context.WithCancel(context.Background())
	defer cancelHigh()
	highPlan, reject, err := o.StartRequest(highCtx, &Request{Input: "high", Priority: RequestPriorityHighest})
	if err != nil {
		t.Fatalf("StartRequest(high): %v", err)
	}
	if reject != nil {
		t.Fatalf("reject(high) = %+v, want nil", reject)
	}

	lowRunner, err := o.Runner(lowPlan.RunnerID)
	if err != nil {
		t.Fatalf("Runner(low): %v", err)
	}
	highRunner, err := o.Runner(highPlan.RunnerID)
	if err != nil {
		t.Fatalf("Runner(high): %v", err)
	}

	lowRelease := make(chan struct{})
	mustNodeExecutor(t, lowRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		<-lowRelease
		return "low", nil, nil
	}
	highStarted := make(chan struct{})
	highRelease := make(chan struct{})
	mustNodeExecutor(t, highRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(highStarted)
		<-highRelease
		return "high", nil, nil
	}
	highAccepted := false

	lowView, err := o.QueryRunner(lowPlan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner(low): %v", err)
	}
	if lowView.State.Status != RunnerStatusPending {
		t.Fatalf("low state = %q, want pending", lowView.State.Status)
	}
	highView, err := o.QueryRunner(highPlan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner(high): %v", err)
	}
	if highView.State.Status != RunnerStatusPending {
		t.Fatalf("high state = %q, want pending", highView.State.Status)
	}

	close(firstRelease)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		highView, err = o.QueryRunner(highPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(high awaiting review): %v", err)
		}
		if highView.Phase == runnerpkg.PhaseAwaitingReview {
			if err := o.AcceptRunnerPlan(highCtx, highPlan.RunnerID, highPlan); err != nil {
				t.Fatalf("AcceptRunnerPlan(high): %v", err)
			}
			highAccepted = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !highAccepted {
		t.Fatal("high priority runner did not reach awaiting review")
	}
	select {
	case <-highStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for high priority runner to start")
	}
	time.Sleep(150 * time.Millisecond)
	lowView, err = o.QueryRunner(lowPlan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner(low after high start): %v", err)
	}
	if lowView.State.Status != RunnerStatusPending {
		t.Fatalf("low state after high start = %q, want pending", lowView.State.Status)
	}

	close(highRelease)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		lowView, err = o.QueryRunner(lowPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(low after high drain): %v", err)
		}
		if lowView.Phase == runnerpkg.PhaseAwaitingReview {
			if err := o.AcceptRunnerPlan(lowCtx, lowPlan.RunnerID, lowPlan); err != nil {
				t.Fatalf("AcceptRunnerPlan(low): %v", err)
			}
		}
		if lowView.State.Status == RunnerStatusRunning || lowView.State.Status == RunnerStatusIdle {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lowView.State.Status != RunnerStatusRunning && lowView.State.Status != RunnerStatusIdle {
		t.Fatalf("low state after high drain = %q, want running or idle", lowView.State.Status)
	}

	close(lowRelease)
	cancelFirst()
	cancelHigh()
	cancelLow()
}

func TestStartRequest_CanceledWhileQueuedTransitionsRunner(t *testing.T) {
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
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	if err := o.StartRunner(firstCtx, firstPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(first): %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(firstPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(first awaiting review): %v", err)
		}
		if view.Phase == runnerpkg.PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.AcceptRunnerPlan(firstCtx, firstPlan.RunnerID, firstPlan); err != nil {
		t.Fatalf("AcceptRunnerPlan(first): %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first runner to start")
	}

	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queuedPlan, reject, err := o.StartRequest(queuedCtx, &Request{Input: "queued", Priority: 2})
	if err != nil {
		t.Fatalf("StartRequest(queued): %v", err)
	}
	if reject != nil {
		t.Fatalf("reject(queued) = %+v, want nil", reject)
	}
	queuedRunner, err := o.Runner(queuedPlan.RunnerID)
	if err != nil {
		t.Fatalf("Runner(queued): %v", err)
	}
	queuedStarted := make(chan struct{})
	mustNodeExecutor(t, queuedRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(queuedStarted)
		return "queued", nil, nil
	}
	updates, unsubscribe, err := o.SubscribeRunner(queuedPlan.RunnerID, 4)
	if err != nil {
		t.Fatalf("SubscribeRunner: %v", err)
	}
	defer unsubscribe()
	waitForRunnerUpdate(t, updates, func(update RunnerUpdate) bool {
		return update.Event == nil && update.State.Status == RunnerStatusPending
	}, "queued initial pending update")

	cancelQueued()
	terminalUpdate := waitForRunnerUpdate(t, updates, func(update RunnerUpdate) bool {
		return update.State.Status == RunnerStatusCanceled
	}, "queued canceled update")
	if terminalUpdate.State.Status != RunnerStatusCanceled {
		t.Fatalf("queued state = %q, want canceled", terminalUpdate.State.Status)
	}
	waitForRunnerUpdateClosed(t, updates, "queued canceled update")

	close(firstRelease)
	select {
	case <-queuedStarted:
		t.Fatal("queued runner started after its submission context was canceled")
	case <-time.After(200 * time.Millisecond):
	}
	view, err := o.QueryRunner(queuedPlan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner(queued): %v", err)
	}
	if view.State.Status != RunnerStatusCanceled {
		t.Fatalf("queued final state = %q, want canceled", view.State.Status)
	}
	cancelFirst()
}

func TestLoadRunnerRecords_LoadsPersistedLog(t *testing.T) {
	o := newOrchestrator(testLogger)
	store := mustNewRunnerStore(t, t.TempDir())
	if err := o.SetRunnerStore(store); err != nil {
		t.Fatalf("SetRunnerStore: %v", err)
	}
	plan, managedRunner := mustPlanSingleNodeRunner(t, o)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	sub := managedRunner.Subscribe(16)
	go func() {
		if err := managedRunner.Start(ctx); err != nil {
			done <- err
			return
		}
		done <- managedRunner.Wait(context.Background())
	}()
	if err := managedRunner.AddNodes(ctx, managedRunner.Nodes()["only"]); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}
	waitForRunnerEvent(t, sub, EventNodeCompleted, "only")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start err = %v, want context.Canceled", err)
	}

	records, err := o.LoadRunnerRecords(context.Background(), plan.RunnerID)
	if err != nil {
		t.Fatalf("LoadRunnerRecords: %v", err)
	}
	if len(records) == 0 || records[0].Kind != RunnerRecordInit {
		t.Fatalf("records = %+v, want init-prefixed log", records)
	}
	if !hasStoredEvent(records, EventNodeCompleted.String(), "only") {
		t.Fatal("missing persisted node-completed event")
	}
}

func TestRemoveRunner_RejectsActiveRunner(t *testing.T) {
	o := newOrchestrator(testLogger)
	store := mustNewRunnerStore(t, t.TempDir())
	if err := o.SetRunnerStore(store); err != nil {
		t.Fatalf("SetRunnerStore: %v", err)
	}
	plan, managedRunner := mustPlanSingleNodeRunner(t, o)

	started := make(chan struct{})
	release := make(chan struct{})
	mustNodeExecutor(t, managedRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(started)
		<-release
		return nil, nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		if err := managedRunner.Start(ctx); err != nil {
			done <- err
			return
		}
		done <- managedRunner.Wait(context.Background())
	}()
	if err := managedRunner.AddNodes(ctx, managedRunner.Nodes()["only"]); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}
	<-started

	if err := o.RemoveRunner(context.Background(), plan.RunnerID); !errors.Is(err, ErrRunnerActive) {
		t.Fatalf("RemoveRunner err = %v, want ErrRunnerActive", err)
	}
	close(release)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start err = %v, want context.Canceled", err)
	}
}

func TestRemoveRunner_DeletesTerminalRunnerAndLog(t *testing.T) {
	o := newOrchestrator(testLogger)
	store := mustNewRunnerStore(t, t.TempDir())
	if err := o.SetRunnerStore(store); err != nil {
		t.Fatalf("SetRunnerStore: %v", err)
	}
	plan, managedRunner := mustPlanSingleNodeRunner(t, o)
	mustNodeExecutor(t, managedRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		return nil, nil, errors.New("boom")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		if err := managedRunner.Start(ctx); err != nil {
			done <- err
			return
		}
		done <- managedRunner.Wait(context.Background())
	}()
	if err := managedRunner.AddNodes(ctx, managedRunner.Nodes()["only"]); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}
	if err := <-done; err == nil {
		t.Fatal("expected runner failure")
	}

	if err := o.RemoveRunner(context.Background(), plan.RunnerID); err != nil {
		t.Fatalf("RemoveRunner: %v", err)
	}
	if _, err := o.Runner(plan.RunnerID); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("Runner err = %v, want ErrRunnerNotFound", err)
	}
	if _, err := store.Load(context.Background(), plan.RunnerID); !errors.Is(err, ErrRunnerLogNotFound) {
		t.Fatalf("store.Load err = %v, want ErrRunnerLogNotFound", err)
	}
}

func TestSetPlanningFunc_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.planFromRequest(&Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if err := o.SetPlanningFunc(func(req *Request, skills map[string]*skill.Skill) (*CoarsePlan, *RejectReason, error) {
		return nil, nil, nil
	}); err == nil {
		t.Fatal("expected SetPlanningFunc to fail after startup")
	}
	if o.planFn == nil {
		t.Fatal("planner should remain configured after startup rejection")
	}
}
