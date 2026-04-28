package orchestrate

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/tsumina/dango/internal/llm"
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
	if _, _, err := o.planFromRequest(context.Background(), &Request{Input: "summarize this repository"}); err != nil {
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
	if _, _, err := o.planFromRequest(context.Background(), &Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if err := o.SetRunnerStore(mustNewRunnerStore(t, t.TempDir())); err == nil {
		t.Fatal("expected SetRunnerStore to fail after startup")
	}
}

func TestSetMaxRunningRunners_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.planFromRequest(context.Background(), &Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if err := o.SetMaxRunningRunners(1); err == nil {
		t.Fatal("expected SetMaxRunningRunners to fail after startup")
	}
}

func TestSetOrchestratorSkill_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.planFromRequest(context.Background(), &Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if err := o.SetOrchestratorSkill(defaultOrchestratorSkill()); err == nil {
		t.Fatal("expected SetOrchestratorSkill to fail after startup")
	}
}

func TestSetOrchestratorSkill_UsesProvidedSkillBeforeStartup(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	sk := defaultOrchestratorSkill()
	sk.Name = "custom"
	if err := o.SetOrchestratorSkill(sk); err != nil {
		t.Fatalf("SetOrchestratorSkill: %v", err)
	}
	if got := o.OrchestratorSkill(); got != sk {
		t.Fatalf("OrchestratorSkill() = %p, want %p", got, sk)
	}
}

func TestSetOrchestratorSkillDir_UsesLoadedSkillBeforeStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	dir := writeTestSkill(t, "custom-orchestrator", "A custom orchestrator skill.")
	if err := o.SetOrchestratorSkillDir(dir); err != nil {
		t.Fatalf("SetOrchestratorSkillDir: %v", err)
	}
	sk := o.OrchestratorSkill()
	if sk == nil {
		t.Fatal("expected orchestrator skill to be configured")
	}
	if sk.Name != "custom-orchestrator" {
		t.Fatalf("Name = %q, want %q", sk.Name, "custom-orchestrator")
	}
	if sk.Dir() == nil {
		t.Fatal("Dir() = nil, want loaded skill filesystem")
	}
}

func TestSetOrchestratorSkillDir_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.planFromRequest(context.Background(), &Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if err := o.SetOrchestratorSkillDir(writeTestSkill(t, "late-orchestrator", "late")); err == nil {
		t.Fatal("expected SetOrchestratorSkillDir to fail after startup")
	}
}

func TestOrchestratorSkill_DefaultsToEmbeddedSkill(t *testing.T) {
	o := newOrchestrator(testLogger)
	sk := o.OrchestratorSkill()
	if sk == nil {
		t.Fatal("expected embedded orchestrator skill")
	}
	if sk.Name != "orchestrator" {
		t.Fatalf("Name = %q, want %q", sk.Name, "orchestrator")
	}
	if sk.Dir() == nil {
		t.Fatal("Dir() = nil, want embedded skill filesystem")
	}
	if sk.Instruction == "" {
		t.Fatal("expected embedded orchestrator instruction to be populated")
	}
}

func TestOrchestratorSkill_IsInitializedWithEnvClientOnConstruction(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	t.Setenv("MODEL", "env-model")
	o := newOrchestrator(testLogger)
	sk := o.OrchestratorSkill()
	if sk == nil {
		t.Fatal("expected orchestrator skill to be configured")
	}
	if sk.Client() == nil {
		t.Fatal("expected orchestrator skill client to be initialized")
	}
	if sk.Conversation() == nil {
		t.Fatal("expected orchestrator skill conversation to be initialized")
	}
	client, err := o.resolveEnvClient()
	if err != nil {
		t.Fatalf("resolveEnvClient: %v", err)
	}
	if sk.Client() != client {
		t.Fatalf("orchestrator skill client = %p, want %p", sk.Client(), client)
	}
	if got := sk.Client().Model(); got != "env-model" {
		t.Fatalf("Model() = %q, want %q", got, "env-model")
	}
}

func TestResolveEnvClient_CachesOrchestratorEnvClient(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	t.Setenv("MODEL", "env-model")
	o := newOrchestrator(testLogger)
	first, err := o.resolveEnvClient()
	if err != nil {
		t.Fatalf("resolveEnvClient(first): %v", err)
	}
	second, err := o.resolveEnvClient()
	if err != nil {
		t.Fatalf("resolveEnvClient(second): %v", err)
	}
	if first == nil || second == nil {
		t.Fatal("resolveEnvClient() returned nil client")
	}
	if first != second {
		t.Fatalf("resolveEnvClient() returned different client pointers: %p vs %p", first, second)
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

func TestAddSkills_LoadsLightweightSkill(t *testing.T) {
	o := newOrchestrator(testLogger)
	dir := writeTestSkill(t, "test-skill", "A skill for orchestrator test.")

	mustAddSkills(t, o, AddSkillConfig{Skill: loadTestSkillFromDir(t, dir), Client: &llm.Client{}})

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
	if sk.Dir() == nil {
		t.Error("Dir() = nil, want registered skill filesystem")
	}
}

func TestAddSkills_StoresRuntimeConfig(t *testing.T) {
	o := newOrchestrator(testLogger)
	client := &llm.Client{}
	convCfg := &llm.ConversationConfig{MaxSteps: 7}
	mustAddSkills(t, o, AddSkillConfig{Skill: loadTestSkillFromDir(t, writeTestSkill(t, "factory-skill", "Configured skill.")), Client: client, Config: convCfg})
	stored := o.skills["factory-skill"]
	if stored.Skill == nil {
		t.Fatal("expected skill config to be stored")
	}
	if stored.Client != client {
		t.Fatalf("stored client = %p, want %p", stored.Client, client)
	}
	if stored.Config == nil || stored.Config.MaxSteps != convCfg.MaxSteps {
		t.Fatalf("stored config = %+v, want MaxSteps=%d", stored.Config, convCfg.MaxSteps)
	}
}

func TestAddSkills_PreservesAccessibleDirs(t *testing.T) {
	o := newOrchestrator(testLogger)
	extraDir := t.TempDir()
	loaded := loadTestSkillFromDir(t, writeTestSkill(t, "accessible-skill", "Configured skill."))
	if err := loaded.WithAccessibleDirs(extraDir); err != nil {
		t.Fatalf("WithAccessibleDirs: %v", err)
	}
	mustAddSkills(t, o, AddSkillConfig{Skill: loaded, Client: &llm.Client{}})
	sk := o.Skills()["accessible-skill"]
	if sk == nil {
		t.Fatal("expected accessible-skill to be registered")
	}
	realExtraDir, err := filepath.EvalSymlinks(extraDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(extraDir): %v", err)
	}
	if got := sk.AccessibleDirs(); len(got) != 1 || got[0] != realExtraDir {
		t.Fatalf("AccessibleDirs() = %v, want [%s]", got, realExtraDir)
	}
}

func TestAddSkills_RejectsDuplicateSkillNames(t *testing.T) {
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o, newTestSkillConfig(t, "duplicate", "first", nil))
	if err := o.AddSkills(newTestSkillConfig(t, "duplicate", "second", nil)); err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
}

func TestAddSkills_AllowsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.planFromRequest(context.Background(), &Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}

	dir := writeTestSkill(t, "late-skill", "Registered after startup.")
	mustAddSkills(t, o, AddSkillConfig{Skill: loadTestSkillFromDir(t, dir), Client: &llm.Client{}})

	sk := o.Skills()["late-skill"]
	if sk == nil {
		t.Fatal("expected late-skill to be registered after startup")
	}
	if sk.Dir() == nil {
		t.Fatal("Dir() = nil, want registered skill filesystem")
	}
}

func TestRemoveSkills_AllowsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o,
		newTestSkillConfig(t, "ephemeral", "Removed after startup.", nil),
		newTestSkillConfig(t, "ephemeral-2", "Also removed after startup.", nil),
	)
	if _, _, err := o.planFromRequest(context.Background(), &Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}

	if err := o.RemoveSkills("ephemeral", "ephemeral-2"); err != nil {
		t.Fatalf("RemoveSkills: %v", err)
	}
	if sk := o.Skills()["ephemeral"]; sk != nil {
		t.Fatal("expected ephemeral to be removed after startup")
	}
	if sk := o.Skills()["ephemeral-2"]; sk != nil {
		t.Fatal("expected ephemeral-2 to be removed after startup")
	}
}

func TestRemoveSkills_RejectsUnknownSkill(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.RemoveSkills("missing"); err == nil {
		t.Fatal("expected RemoveSkills to fail for an unknown skill")
	}
}

func TestRemoveSkills_IsAtomicOnValidationFailure(t *testing.T) {
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o,
		newTestSkillConfig(t, "kept", "Must remain registered.", nil),
		newTestSkillConfig(t, "removed", "Would be removed if validation passed.", nil),
	)

	if err := o.RemoveSkills("removed", "missing"); err == nil {
		t.Fatal("expected RemoveSkills to fail when one requested skill is unknown")
	}
	if sk := o.Skills()["removed"]; sk == nil {
		t.Fatal("expected removed to remain registered after atomic validation failure")
	}
	if sk := o.Skills()["kept"]; sk == nil {
		t.Fatal("expected kept to remain registered after atomic validation failure")
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
