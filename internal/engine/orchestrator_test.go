package engine

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
	sqlitepkg "github.com/tsumina/dango/internal/store/sqlite"
)

func TestNewOrchestrator_ReturnsIndependentInstances(t *testing.T) {
	o1 := NewOrchestrator(WithOrchestratorContext(context.Background()))
	o2 := NewOrchestrator(WithOrchestratorContext(context.Background()))
	if o1 == o2 {
		t.Fatal("NewOrchestrator() returned the same instance twice")
	}
	if o1.logger != slog.Default() {
		t.Fatalf("logger = %p, want %p", o1.logger, slog.Default())
	}
}

func TestNewOrchestrator_UsesProvidedContext(t *testing.T) {
	baseCtx, cancel := context.WithCancel(context.Background())
	o := NewOrchestrator(WithOrchestratorContext(baseCtx), WithOrchestratorLogger(testLogger))
	ctx := o.operationContext(context.WithValue(context.Background(), testContextKey("key"), "value"))
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("operation context did not inherit base cancellation")
	}
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx.Err() = %v, want %v", err, context.Canceled)
	}
	if got := ctx.Value(testContextKey("key")); got != "value" {
		t.Fatalf("Value(key) = %v, want value", got)
	}
}

func TestOperationContext_ReturnsAlreadyMergedContext(t *testing.T) {
	o := NewOrchestrator(WithOrchestratorContext(context.Background()), WithOrchestratorLogger(testLogger))
	ctx := o.operationContext(context.WithValue(context.Background(), testContextKey("key"), "value"))
	if got := o.operationContext(ctx); got != ctx {
		t.Fatalf("operationContext(already merged) = %T %p, want original %T %p", got, got, ctx, ctx)
	}
}

func TestMergedContextErrKeepsFirstCancellationReason(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	child, cancelChild := context.WithTimeout(context.Background(), 10*time.Millisecond)
	t.Cleanup(cancelChild)

	ctx := ctxWithValues(parent, child)
	cancelParent()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("merged context did not observe parent cancellation")
	}
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Err() after parent cancellation = %v, want %v", err, context.Canceled)
	}
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("child context did not time out")
	}
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Err() after later child cancellation = %v, want stable %v", err, context.Canceled)
	}
}

func TestSetLogger_NilRestoresDefaultLogger(t *testing.T) {
	o := NewOrchestrator(WithOrchestratorContext(context.Background()))
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

type testContextKey string

func TestSetLogger_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	mustRejectStartRequest(t, o)
	if err := o.SetLogger(newDiscardLogger()); err == nil {
		t.Fatal("expected SetLogger to fail after startup")
	}
	if got := o.logger; got != testLogger {
		t.Fatalf("logger = %p, want %p", got, testLogger)
	}
}

func TestNewOrchestrator_InstallsPersistenceStoresFromOptions(t *testing.T) {
	runnerStore := mustNewRunnerStore(t, t.TempDir())
	eventLogStore := newBlockingEventLogStore()
	cursorStore := &stubSnapshotCursorStore{}
	backend := newTestPersistenceBackend(
		func(b *testPersistenceBackend) { b.runnerLog = runnerStore },
		func(b *testPersistenceBackend) { b.eventLog = eventLogStore },
		func(b *testPersistenceBackend) { b.cursor = cursorStore },
		func(b *testPersistenceBackend) { b.root = t.TempDir() },
	)
	o := newOrchestrator(testLogger,
		WithPersistence(backend),
	)
	if o.persistence != backend {
		t.Fatalf("persistence = %v, want %v", o.persistence, backend)
	}
}

func TestSetMaxRunningRunners_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	mustRejectStartRequest(t, o)
	if err := o.SetMaxRunningRunners(1); err == nil {
		t.Fatal("expected SetMaxRunningRunners to fail after startup")
	}
}

func TestSetOrchestratorSkill_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	mustRejectStartRequest(t, o)
	if err := o.SetOrchestratorSkill(defaultOrchestratorSkill()); err == nil {
		t.Fatal("expected SetOrchestratorSkill to fail after startup")
	}
}

func TestSetClient_BindsDefaultOrchestratorSkill(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	client := &llm.Client{}
	if err := o.SetClient(client); err != nil {
		t.Fatalf("SetClient: %v", err)
	}
	sk := o.OrchestratorSkill()
	if sk.Client() != client {
		t.Fatalf("orchestrator skill client = %p, want %p", sk.Client(), client)
	}
	resolved, err := o.resolveEnvClient()
	if err != nil {
		t.Fatalf("resolveEnvClient: %v", err)
	}
	if resolved != client {
		t.Fatalf("resolved client = %p, want %p", resolved, client)
	}
}

func TestSetClient_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	mustRejectStartRequest(t, o)
	if err := o.SetClient(&llm.Client{}); err == nil {
		t.Fatal("expected SetClient to fail after startup")
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
	mustRejectStartRequest(t, o)
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

func TestWaitRunner_ReturnsFinalView(t *testing.T) {
	o := newOrchestrator(testLogger)
	managedRunner := newManagedQueueTestRunner(t, "wait", func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		return "done", nil, nil
	})
	o.mu.Lock()
	o.runners[managedRunner.ID()] = managedRunner
	o.mu.Unlock()
	go o.watchRunnerDone(managedRunner)

	if err := o.StartRunner(context.Background(), managedRunner.ID()); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	view, err := o.WaitRunner(waitCtx, managedRunner.ID())
	if err != nil {
		t.Fatalf("WaitRunner: %v", err)
	}
	if view == nil {
		t.Fatal("WaitRunner view = nil")
	}
	if view.Phase != PhaseSettled {
		t.Fatalf("phase = %q, want settled", view.Phase)
	}
	if got := view.Snapshot.CompletedNodes["only"]; got != "done" {
		t.Fatalf("completed output = %v, want done", got)
	}
}

func TestWaitRunner_RejectsUnknownID(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, err := o.WaitRunner(context.Background(), "missing"); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("WaitRunner err = %v, want ErrRunnerNotFound", err)
	}
}

func TestWaitRunner_ReturnsViewWhenContextEndsFirst(t *testing.T) {
	o := newOrchestrator(testLogger)
	started := make(chan struct{})
	release := make(chan struct{})
	managedRunner := newManagedQueueTestRunner(t, "wait timeout", func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(started)
		<-release
		return "done", nil, nil
	})
	o.mu.Lock()
	o.runners[managedRunner.ID()] = managedRunner
	o.mu.Unlock()
	go o.watchRunnerDone(managedRunner)

	if err := o.StartRunner(context.Background(), managedRunner.ID()); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	waitForClosed(t, started, "runner started")

	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	view, err := o.WaitRunner(waitCtx, managedRunner.ID())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitRunner err = %v, want context deadline exceeded", err)
	}
	if view == nil || view.RunnerID != managedRunner.ID() {
		t.Fatalf("WaitRunner view = %+v, want current runner view", view)
	}

	close(release)
	waitForRunnerDone(t, managedRunner, "runner done after timeout test")
}

func TestAddSkills_LoadsLightweightSkill(t *testing.T) {
	o := newOrchestrator(testLogger)
	dir := writeTestSkill(t, "test-skill", "A skill for orchestrator test.")

	mustAddSkills(t, o, SkillRegistration{Skill: loadTestSkillFromDir(t, dir), Client: &llm.Client{}})

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

func TestAddSkillDirs_LoadsAndEquipsSkill(t *testing.T) {
	o := newOrchestrator(testLogger)
	client := &llm.Client{}
	cfg := llm.ConversationConfig{MaxSteps: 7}
	dir := writeTestSkill(t, "dir-skill", "A skill loaded by directory.")

	if err := o.SetClient(client); err != nil {
		t.Fatalf("SetClient: %v", err)
	}
	if err := o.AddSkillDirs(cfg, dir); err != nil {
		t.Fatalf("AddSkillDirs: %v", err)
	}

	stored := o.skills["dir-skill"]
	if stored.Skill == nil {
		t.Fatal("expected dir-skill to be registered")
	}
	if stored.Client != client {
		t.Fatalf("stored client = %p, want %p", stored.Client, client)
	}
	if stored.Config.MaxSteps != cfg.MaxSteps {
		t.Fatalf("stored config = %+v, want MaxSteps=%d", stored.Config, cfg.MaxSteps)
	}
	bound, err := stored.Skill.Bind(stored.Client, stored.Config)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	tools := make(map[string]bool)
	for _, spec := range bound.Conversation().Tools() {
		tools[spec.Name] = true
	}
	for _, name := range []string{"bash", "read_file", "write_file", "grep"} {
		if !tools[name] {
			t.Fatalf("runtime tool %q missing from skill tools: %v", name, tools)
		}
	}
}

func TestAddSkills_StoresRuntimeConfig(t *testing.T) {
	o := newOrchestrator(testLogger)
	client := &llm.Client{}
	convCfg := llm.ConversationConfig{MaxSteps: 7}
	mustAddSkills(t, o, SkillRegistration{Skill: loadTestSkillFromDir(t, writeTestSkill(t, "factory-skill", "Configured skill.")), Client: client, Config: convCfg})
	stored := o.skills["factory-skill"]
	if stored.Skill == nil {
		t.Fatal("expected skill config to be stored")
	}
	if stored.Client != client {
		t.Fatalf("stored client = %p, want %p", stored.Client, client)
	}
	if stored.Config.MaxSteps != convCfg.MaxSteps {
		t.Fatalf("stored config = %+v, want MaxSteps=%d", stored.Config, convCfg.MaxSteps)
	}
}

func TestAddSkills_PreservesAccessibleDirs(t *testing.T) {
	o := newOrchestrator(testLogger)
	extraDir := t.TempDir()
	loaded := loadTestSkillFromDir(t, writeTestSkill(t, "accessible-skill", "Configured skill."))
	mustAddSkills(t, o, SkillRegistration{Skill: loaded, AccessibleDirs: []string{extraDir}, Client: &llm.Client{}})
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
	if got := o.skills["accessible-skill"].AccessibleDirs; len(got) != 1 || got[0] != extraDir {
		t.Fatalf("stored AccessibleDirs = %v, want [%s]", got, extraDir)
	}
	if got := loaded.AccessibleDirs(); len(got) != 0 {
		t.Fatalf("source skill AccessibleDirs() = %v, want none", got)
	}
}

func TestAddSkills_EquipsSkillForAutonomousGlueCode(t *testing.T) {
	o := newOrchestrator(testLogger)
	extraDir := t.TempDir()
	loaded := loadTestSkillFromDir(t, writeTestSkill(t, "glue-skill", "Can adapt execution."))
	mustAddSkills(t, o, SkillRegistration{Skill: loaded, AccessibleDirs: []string{extraDir}, Client: &llm.Client{}})

	stored := o.skills["glue-skill"]
	bound, err := stored.Skill.Bind(stored.Client, stored.Config)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	tools := make(map[string]bool)
	for _, spec := range bound.Conversation().Tools() {
		tools[spec.Name] = true
	}
	for _, name := range []string{"bash", "read_file", "write_file", "edit_file", "grep"} {
		if !tools[name] {
			t.Fatalf("runtime tool %q missing from skill tools: %v", name, tools)
		}
	}
	instructions := bound.Conversation().Instructions()
	for _, want := range []string{
		"Workspace access:",
		"Temp playground:",
		"Relative file paths and shell commands run here",
		"User-added directories",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("runtime instructions missing %q:\n%s", want, instructions)
		}
	}
}

func TestAddSkills_RejectsDuplicateSkillNames(t *testing.T) {
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o, newTestSkillRegistration(t, "duplicate", "first", nil))
	if err := o.AddSkills(newTestSkillRegistration(t, "duplicate", "second", nil)); err == nil {
		t.Fatal("expected duplicate registration to fail")
	}
}

func TestAddSkills_AllowsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	mustRejectStartRequest(t, o)

	dir := writeTestSkill(t, "late-skill", "Registered after startup.")
	mustAddSkills(t, o, SkillRegistration{Skill: loadTestSkillFromDir(t, dir), Client: &llm.Client{}})

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
		newTestSkillRegistration(t, "ephemeral", "Removed after startup.", nil),
		newTestSkillRegistration(t, "ephemeral-2", "Also removed after startup.", nil),
	)
	mustRejectStartRequest(t, o)

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
		newTestSkillRegistration(t, "kept", "Must remain registered.", nil),
		newTestSkillRegistration(t, "removed", "Would be removed if validation passed.", nil),
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

func TestLoadRunnerRecords_LoadsPersistedLogWithoutLiveRunner(t *testing.T) {
	store := mustNewRunnerStore(t, t.TempDir())
	o := newOrchestrator(testLogger, WithPersistence(newTestPersistenceBackend(
		func(b *testPersistenceBackend) { b.runnerLog = store },
		func(b *testPersistenceBackend) { b.root = t.TempDir() },
	)))
	if _, err := store.Append(context.Background(), "runner_persisted_only", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}); err != nil {
		t.Fatalf("Append(init): %v", err)
	}
	if _, err := store.Append(context.Background(), "runner_persisted_only", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordStatus, Status: runnerpkg.RunnerStatusIdle}); err != nil {
		t.Fatalf("Append(status): %v", err)
	}

	records, err := o.LoadRunnerRecords(context.Background(), "runner_persisted_only")
	if err != nil {
		t.Fatalf("LoadRunnerRecords: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
	if records[0].Kind != runnerpkg.RunnerRecordInit {
		t.Fatalf("records[0].Kind = %q, want init", records[0].Kind)
	}
}

func TestStartRunner_ForwardsStreamAndQueryState(t *testing.T) {
	o := newOrchestrator(testLogger)
	started := make(chan struct{})
	release := make(chan struct{})

	plan := &CoarsePlan{
		Request: "stream",
		Nodes:   []CoarsePlanNode{{ID: "only", SkillName: "single", TaskDescription: "stream"}},
	}
	nodes := map[string]*runnerpkg.Node{
		"only": {
			Id: "only",
			Agent: &stubRunnerAgent{
				polish: func(ctx context.Context) (any, error) { return "stream polish", nil },
				execute: func(ctx context.Context, parentOutputs map[string]any) (any, []*runnerpkg.Node, error) {
					close(started)
					<-release
					return "done", nil, nil
				},
				report: func(ctx context.Context, output any) (any, error) { return output, nil },
			},
		},
	}
	managedRunner := runnerpkg.New(
		runnerpkg.WithContext(context.Background()),
		runnerpkg.WithLogger(testLogger),
		runnerpkg.WithInitialPlan(plan, nodes),
		runnerpkg.WithPlannerSkill(bindTestOrchestratorSkill(t, mustReviewJSON(t, true, ""))),
		runnerpkg.WithSkillSummaries([]runnerpkg.SkillSummary{{Name: "single", Description: "Single test skill."}}),
		runnerpkg.WithPlanNodeBuilder(func(plan *runnerpkg.CoarsePlan) (map[string]*runnerpkg.Node, error) { return nodes, nil }),
	)

	o.mu.Lock()
	o.runners[managedRunner.ID()] = managedRunner
	o.mu.Unlock()
	go o.watchRunnerDone(managedRunner)

	sub, err := o.SubscribeRunnerStream(managedRunner.ID(), streampkg.Filter{}, streampkg.WithReplayLast(64), streampkg.WithSubscriberBuffer(64))
	if err != nil {
		t.Fatalf("SubscribeRunnerStream: %v", err)
	}
	defer sub.Cancel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := o.StartRunner(ctx, managedRunner.ID()); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runner to start")
	}

	// Wait for runner.node.started stream event.
	waitForStreamEvent(t, sub, streampkg.EventRunnerNodeStarted, "node started")

	view, err := o.QueryRunner(managedRunner.ID())
	if err != nil {
		t.Fatalf("QueryRunner: %v", err)
	}
	if view.State.Status != RunnerStatusRunning {
		t.Fatalf("queried state = %q, want running", view.State.Status)
	}

	close(release)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	if err := managedRunner.Wait(waitCtx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	finalView, err := o.QueryRunner(managedRunner.ID())
	if err != nil {
		t.Fatalf("QueryRunner(final): %v", err)
	}
	if finalView.Phase != PhaseSettled {
		t.Fatalf("final queried phase = %q, want settled", finalView.Phase)
	}
}

func TestLoadRunnerRecords_LoadsPersistedLog(t *testing.T) {
	testLoadRunnerRecordsLoadsPersistedLog(t, func(t *testing.T) OrchestratorOption {
		t.Helper()
		store := mustNewRunnerStore(t, t.TempDir())
		return WithPersistence(newTestPersistenceBackend(
			func(b *testPersistenceBackend) { b.runnerLog = store },
			func(b *testPersistenceBackend) { b.root = t.TempDir() },
		))
	})
}

func TestLoadRunnerRecords_LoadsPersistedSQLiteLog(t *testing.T) {
	testLoadRunnerRecordsLoadsPersistedLog(t, func(t *testing.T) OrchestratorOption {
		t.Helper()
		dbStore, err := sqlitepkg.Open(filepath.Join(t.TempDir(), "dango.db"))
		if err != nil {
			t.Fatalf("Open sqlite store: %v", err)
		}
		t.Cleanup(func() {
			if err := dbStore.Close(); err != nil {
				t.Fatalf("Close sqlite store: %v", err)
			}
		})
		return WithPersistence(newTestPersistenceBackend(
			func(b *testPersistenceBackend) { b.runnerLog = sqlitepkg.NewRunnerStore(dbStore) },
			func(b *testPersistenceBackend) { b.root = t.TempDir() },
		))
	})
}

func testLoadRunnerRecordsLoadsPersistedLog(t *testing.T, configureStore func(t *testing.T) OrchestratorOption) {
	t.Helper()

	o := newOrchestrator(testLogger, configureStore(t))
	mustAddSkills(t, o, newTestSkillRegistration(t, "single", "Single-step runner.", nil))
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, mustPlanJSON(t, &CoarsePlan{
		Request: "run a single node",
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node.",
		}},
	}), mustReviewJSON(t, true, ""))); err != nil {
		t.Fatalf("SetOrchestratorSkill: %v", err)
	}
	resp, err := o.StartRequest(context.Background(), Request{Input: "run a single node"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	runnerID := mustReadRunnerCreated(t, resp.Stream)
	managedRunner, err := o.Runner(runnerID)
	if err != nil {
		t.Fatalf("Runner: %v", err)
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	if err := managedRunner.Wait(waitCtx); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	records, err := o.LoadRunnerRecords(context.Background(), runnerID)
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
	store := mustNewRunnerStore(t, t.TempDir())
	o := newOrchestrator(testLogger, WithPersistence(newTestPersistenceBackend(
		func(b *testPersistenceBackend) { b.runnerLog = store },
		func(b *testPersistenceBackend) { b.root = t.TempDir() },
	)))
	started := make(chan struct{})
	release := make(chan struct{})
	managedRunner := newManagedQueueTestRunner(t, "active", func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(started)
		<-release
		return nil, nil, nil
	})
	o.mu.Lock()
	o.runners[managedRunner.ID()] = managedRunner
	o.mu.Unlock()
	go o.watchRunnerDone(managedRunner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := o.StartRunner(ctx, managedRunner.ID()); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	<-started

	if err := o.RemoveRunner(context.Background(), managedRunner.ID()); !errors.Is(err, ErrRunnerActive) {
		t.Fatalf("RemoveRunner err = %v, want ErrRunnerActive", err)
	}
	close(release)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	if err := managedRunner.Wait(waitCtx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestRemoveRunner_DeletesTerminalRunnerAndLog(t *testing.T) {
	store := mustNewRunnerStore(t, t.TempDir())
	o := newOrchestrator(testLogger, WithPersistence(newTestPersistenceBackend(
		func(b *testPersistenceBackend) { b.runnerLog = store },
		func(b *testPersistenceBackend) { b.root = t.TempDir() },
	)))
	managedRunner := newManagedQueueTestRunner(t, "failing", func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		return nil, nil, errors.New("boom")
	})
	o.mu.Lock()
	o.runners[managedRunner.ID()] = managedRunner
	o.mu.Unlock()
	go o.watchRunnerDone(managedRunner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := o.StartRunner(ctx, managedRunner.ID()); err != nil {
		t.Fatalf("StartRunner: %v", err)
	}
	if err := managedRunner.Wait(context.Background()); err == nil {
		t.Fatal("expected runner failure")
	}

	if err := o.RemoveRunner(context.Background(), managedRunner.ID()); err != nil {
		t.Fatalf("RemoveRunner: %v", err)
	}
	if _, err := o.Runner(managedRunner.ID()); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("Runner err = %v, want ErrRunnerNotFound", err)
	}
	if _, err := store.Load(context.Background(), managedRunner.ID()); !errors.Is(err, ErrRunnerLogNotFound) {
		t.Fatalf("store.Load err = %v, want ErrRunnerLogNotFound", err)
	}
}
