package orchestrate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/llm/skill"
)

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

func TestSetLLMClient_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.planFromRequest(&Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if err := o.SetLLMClient(&llm.Client{}); err == nil {
		t.Fatal("expected SetLLMClient to fail after startup")
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

func TestRegisterSkill_StoresPerSkillClientFactory(t *testing.T) {
	o := newOrchestrator(testLogger)
	client := &llm.Client{}
	if err := o.RegisterSkill(writeTestSkill(t, "factory-skill", "Configured skill."), WithSkillClientFactory(func() (*llm.Client, error) {
		return client, nil
	})); err != nil {
		t.Fatalf("RegisterSkill: %v", err)
	}
	factory := o.skillClientByName["factory-skill"]
	if factory == nil {
		t.Fatal("expected per-skill client factory to be stored")
	}
	got, err := factory()
	if err != nil {
		t.Fatalf("factory(): %v", err)
	}
	if got != client {
		t.Fatalf("factory() = %p, want %p", got, client)
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
		return update.Phase == PhaseAwaitingReview
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
