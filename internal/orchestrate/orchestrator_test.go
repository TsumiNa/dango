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

func mustPlanSingleNodeRuntime(t *testing.T, o *Orchestrator) (*CoarsePlan, *ManagedRuntime) {
	t.Helper()
	if err := o.RegisterSkill(writeTestSkill(t, "single", "Single-step runtime.")); err != nil {
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
	managedRuntime, ok := o.Runtimes()[plan.RuntimeID]
	if !ok || managedRuntime == nil {
		t.Fatalf("expected runtime %q to be stored", plan.RuntimeID)
	}
	return plan, managedRuntime
}

func waitForRuntimeUpdate(t *testing.T, ch <-chan RuntimeUpdate, predicate func(RuntimeUpdate) bool, label string) RuntimeUpdate {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatalf("timed out waiting for runtime update: %s", label)
		case update, ok := <-ch:
			if !ok {
				t.Fatalf("runtime update stream closed while waiting for %s", label)
			}
			if predicate(update) {
				return update
			}
		}
	}
}

func waitForRuntimeUpdateClosed(t *testing.T, ch <-chan RuntimeUpdate, label string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("runtime update stream still open while waiting for %s", label)
		}
	case <-timer.C:
		t.Fatalf("timed out waiting for runtime update stream to close: %s", label)
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

func TestSetRuntimeStore_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.planFromRequest(&Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if err := o.SetRuntimeStore(mustNewRuntimeStore(t, t.TempDir())); err == nil {
		t.Fatal("expected SetRuntimeStore to fail after startup")
	}
}

func TestSetMaxRunningRuntimes_RejectsChangesAfterStartup(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.planFromRequest(&Request{Input: "summarize this repository"}); err != nil {
		t.Fatalf("planFromRequest: %v", err)
	}
	if err := o.SetMaxRunningRuntimes(1); err == nil {
		t.Fatal("expected SetMaxRunningRuntimes to fail after startup")
	}
}

func TestRuntime_ReturnsManagedRuntimeByID(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, managedRuntime := mustPlanSingleNodeRuntime(t, o)

	got, err := o.Runtime(plan.RuntimeID)
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	if got != managedRuntime {
		t.Fatalf("Runtime() = %p, want %p", got, managedRuntime)
	}
}

func TestRuntime_RejectsUnknownID(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, err := o.Runtime("missing"); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("Runtime err = %v, want ErrRuntimeNotFound", err)
	}
}

func TestQueryRuntime_ReturnsRuntimeView(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRuntime(t, o)

	view, err := o.QueryRuntime(plan.RuntimeID)
	if err != nil {
		t.Fatalf("QueryRuntime: %v", err)
	}
	if view.RuntimeID != plan.RuntimeID {
		t.Fatalf("RuntimeID = %q, want %q", view.RuntimeID, plan.RuntimeID)
	}
	if view.Plan == nil || view.Plan.Request != plan.Request {
		t.Fatalf("Plan = %+v, want request %q", view.Plan, plan.Request)
	}
	if view.State.Status != RuntimeStatusPending {
		t.Fatalf("state = %q, want pending", view.State.Status)
	}
	if _, ok := view.Snapshot.NodesData["only"]; !ok {
		t.Fatal("expected query snapshot to include the only node")
	}
}

func TestSubscribeRuntime_RejectsUnknownID(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, _, err := o.SubscribeRuntime("missing", 4); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("SubscribeRuntime err = %v, want ErrRuntimeNotFound", err)
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
	if len(o.Runtimes()) != 0 {
		t.Fatalf("expected no runtimes to be created on rejection")
	}
}

func TestPlanFromRequest_BuildsRuntimeFromPlan(t *testing.T) {
	o := newOrchestrator(testLogger)
	store := mustNewRuntimeStore(t, t.TempDir())
	if err := o.SetRuntimeStore(store); err != nil {
		t.Fatalf("SetRuntimeStore: %v", err)
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
	if plan.RuntimeID == "" {
		t.Fatal("expected coarse plan to be annotated with a runtime ID")
	}

	managedRuntime := o.Runtimes()[plan.RuntimeID]
	if managedRuntime == nil {
		t.Fatalf("expected runtime %q to be stored", plan.RuntimeID)
	}
	if managedRuntime.Runtime.ID() != plan.RuntimeID {
		t.Errorf("Runtime.ID() = %q, want %q", managedRuntime.Runtime.ID(), plan.RuntimeID)
	}
	if managedRuntime.Runtime.store != store {
		t.Fatalf("runtime store = %p, want %p", managedRuntime.Runtime.store, store)
	}
	if len(managedRuntime.Nodes) != 2 {
		t.Fatalf("len(Nodes) = %d, want 2", len(managedRuntime.Nodes))
	}

	draft := managedRuntime.Nodes["draft"]
	run := managedRuntime.Nodes["run"]
	if draft == nil || run == nil {
		t.Fatalf("expected draft and run nodes to exist, got draft=%v run=%v", draft, run)
	}
	if draft.Executor == nil || draft.Executor.Skill().Name != "plan" {
		t.Fatalf("draft executor skill = %v, want plan", draft.Executor)
	}
	if run.Executor == nil || run.Executor.Skill().Name != "execute" {
		t.Fatalf("run executor skill = %v, want execute", run.Executor)
	}
	if len(run.Parents) != 1 || run.Parents[0].Id != "draft" {
		t.Fatalf("run parents = %+v, want [draft]", run.Parents)
	}
	if got := run.Executor.Planner().TaskDescription; got != "Execute the approved outline." {
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
	if len(o.Runtimes()) != 0 {
		t.Fatalf("expected no runtimes to be stored when plan assembly fails")
	}
}

func TestLoadRuntimeRecords_RequiresConfiguredStore(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, _ := mustPlanSingleNodeRuntime(t, o)

	_, err := o.LoadRuntimeRecords(context.Background(), plan.RuntimeID)
	if !errors.Is(err, ErrRuntimeStoreNotConfigured) {
		t.Fatalf("LoadRuntimeRecords err = %v, want ErrRuntimeStoreNotConfigured", err)
	}
}

func TestStartRuntime_ForwardsStreamAndQueryState(t *testing.T) {
	o := newOrchestrator(testLogger)
	plan, managedRuntime := mustPlanSingleNodeRuntime(t, o)

	started := make(chan struct{})
	release := make(chan struct{})
	managedRuntime.Nodes["only"].Executor.RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(started)
		<-release
		return "done", nil, nil
	}

	updates, unsubscribe, err := o.SubscribeRuntime(plan.RuntimeID, 8)
	if err != nil {
		t.Fatalf("SubscribeRuntime: %v", err)
	}
	defer unsubscribe()

	initial := waitForRuntimeUpdate(t, updates, func(update RuntimeUpdate) bool {
		return update.Event == nil
	}, "initial update")
	if initial.State.Status != RuntimeStatusPending {
		t.Fatalf("initial state = %q, want pending", initial.State.Status)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := o.StartRuntime(ctx, plan.RuntimeID); err != nil {
		t.Fatalf("StartRuntime: %v", err)
	}
	<-started

	startedUpdate := waitForRuntimeUpdate(t, updates, func(update RuntimeUpdate) bool {
		return update.Event != nil && update.Event.Type == EventNodeStarted
	}, "node started")
	if startedUpdate.State.Status != RuntimeStatusRunning {
		t.Fatalf("state after node started = %q, want running", startedUpdate.State.Status)
	}

	view, err := o.QueryRuntime(plan.RuntimeID)
	if err != nil {
		t.Fatalf("QueryRuntime: %v", err)
	}
	if view.State.Status != RuntimeStatusRunning {
		t.Fatalf("queried state = %q, want running", view.State.Status)
	}

	close(release)
	completedUpdate := waitForRuntimeUpdate(t, updates, func(update RuntimeUpdate) bool {
		return update.Event != nil && update.Event.Type == EventNodeCompleted
	}, "node completed")
	if completedUpdate.Event.NodeID != "only" {
		t.Fatalf("completed node = %q, want only", completedUpdate.Event.NodeID)
	}
	idleUpdate := waitForRuntimeUpdate(t, updates, func(update RuntimeUpdate) bool {
		return update.Event != nil && update.Event.Type == EventEngineIdle
	}, "engine idle")
	if idleUpdate.State.Status != RuntimeStatusIdle {
		t.Fatalf("idle state = %q, want idle", idleUpdate.State.Status)
	}

	cancel()
	terminalUpdate := waitForRuntimeUpdate(t, updates, func(update RuntimeUpdate) bool {
		return update.State.Status == RuntimeStatusCanceled
	}, "canceled terminal update")
	if terminalUpdate.State.Status != RuntimeStatusCanceled {
		t.Fatalf("terminal state = %q, want canceled", terminalUpdate.State.Status)
	}
	waitForRuntimeUpdateClosed(t, updates, "canceled terminal update")
	finalView, err := o.QueryRuntime(plan.RuntimeID)
	if err != nil {
		t.Fatalf("QueryRuntime(final): %v", err)
	}
	if finalView.State.Status != RuntimeStatusCanceled {
		t.Fatalf("final queried state = %q, want canceled", finalView.State.Status)
	}
}

func TestStartRequest_StartsRuntimeImmediately(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.RegisterSkill(writeTestSkill(t, "single", "Single-step runtime.")); err != nil {
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
		view, queryErr := o.QueryRuntime(plan.RuntimeID)
		if queryErr != nil {
			t.Fatalf("QueryRuntime: %v", queryErr)
		}
		if view.State.Status == RuntimeStatusRunning || view.State.Status == RuntimeStatusIdle {
			cancel()
			finalDeadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(finalDeadline) {
				finalView, finalErr := o.QueryRuntime(plan.RuntimeID)
				if finalErr != nil {
					t.Fatalf("QueryRuntime(final): %v", finalErr)
				}
				if finalView.State.Status == RuntimeStatusCanceled {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("runtime did not reach canceled after StartRequest context cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("runtime did not start after StartRequest")
}

func TestStartRequest_RejectsPriorityOutsideRange(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.RegisterSkill(writeTestSkill(t, "single", "Single-step runtime.")); err != nil {
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
		if len(o.Runtimes()) != 0 {
			t.Fatalf("expected no runtimes to be created for invalid priority %d", priority)
		}
	}
}

func TestStartRequest_QueuesByPriorityWhenLimitReached(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetMaxRunningRuntimes(1); err != nil {
		t.Fatalf("SetMaxRunningRuntimes: %v", err)
	}

	firstPlan, firstRuntime := mustPlanSingleNodeRuntime(t, o)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	firstRuntime.Nodes["only"].Executor.RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(firstStarted)
		<-firstRelease
		return "first", nil, nil
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	if err := o.StartRuntime(firstCtx, firstPlan.RuntimeID); err != nil {
		t.Fatalf("StartRuntime(first): %v", err)
	}
	<-firstStarted

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

	lowRuntime, err := o.Runtime(lowPlan.RuntimeID)
	if err != nil {
		t.Fatalf("Runtime(low): %v", err)
	}
	highRuntime, err := o.Runtime(highPlan.RuntimeID)
	if err != nil {
		t.Fatalf("Runtime(high): %v", err)
	}

	lowRelease := make(chan struct{})
	lowRuntime.Nodes["only"].Executor.RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		<-lowRelease
		return "low", nil, nil
	}
	highStarted := make(chan struct{})
	highRelease := make(chan struct{})
	highRuntime.Nodes["only"].Executor.RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(highStarted)
		<-highRelease
		return "high", nil, nil
	}

	lowView, err := o.QueryRuntime(lowPlan.RuntimeID)
	if err != nil {
		t.Fatalf("QueryRuntime(low): %v", err)
	}
	if lowView.State.Status != RuntimeStatusPending {
		t.Fatalf("low state = %q, want pending", lowView.State.Status)
	}
	highView, err := o.QueryRuntime(highPlan.RuntimeID)
	if err != nil {
		t.Fatalf("QueryRuntime(high): %v", err)
	}
	if highView.State.Status != RuntimeStatusPending {
		t.Fatalf("high state = %q, want pending", highView.State.Status)
	}

	close(firstRelease)
	select {
	case <-highStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for high priority runtime to start")
	}
	time.Sleep(150 * time.Millisecond)
	lowView, err = o.QueryRuntime(lowPlan.RuntimeID)
	if err != nil {
		t.Fatalf("QueryRuntime(low after high start): %v", err)
	}
	if lowView.State.Status != RuntimeStatusPending {
		t.Fatalf("low state after high start = %q, want pending", lowView.State.Status)
	}

	close(highRelease)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		lowView, err = o.QueryRuntime(lowPlan.RuntimeID)
		if err != nil {
			t.Fatalf("QueryRuntime(low after high drain): %v", err)
		}
		if lowView.State.Status == RuntimeStatusRunning || lowView.State.Status == RuntimeStatusIdle {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lowView.State.Status != RuntimeStatusRunning && lowView.State.Status != RuntimeStatusIdle {
		t.Fatalf("low state after high drain = %q, want running or idle", lowView.State.Status)
	}

	close(lowRelease)
	cancelFirst()
	cancelHigh()
	cancelLow()
}

func TestStartRequest_CanceledWhileQueuedTransitionsRuntime(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetMaxRunningRuntimes(1); err != nil {
		t.Fatalf("SetMaxRunningRuntimes: %v", err)
	}

	firstPlan, firstRuntime := mustPlanSingleNodeRuntime(t, o)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	firstRuntime.Nodes["only"].Executor.RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(firstStarted)
		<-firstRelease
		return "first", nil, nil
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	if err := o.StartRuntime(firstCtx, firstPlan.RuntimeID); err != nil {
		t.Fatalf("StartRuntime(first): %v", err)
	}
	<-firstStarted

	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queuedPlan, reject, err := o.StartRequest(queuedCtx, &Request{Input: "queued", Priority: 2})
	if err != nil {
		t.Fatalf("StartRequest(queued): %v", err)
	}
	if reject != nil {
		t.Fatalf("reject(queued) = %+v, want nil", reject)
	}
	queuedRuntime, err := o.Runtime(queuedPlan.RuntimeID)
	if err != nil {
		t.Fatalf("Runtime(queued): %v", err)
	}
	queuedStarted := make(chan struct{})
	queuedRuntime.Nodes["only"].Executor.RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(queuedStarted)
		return "queued", nil, nil
	}
	updates, unsubscribe, err := o.SubscribeRuntime(queuedPlan.RuntimeID, 4)
	if err != nil {
		t.Fatalf("SubscribeRuntime: %v", err)
	}
	defer unsubscribe()
	waitForRuntimeUpdate(t, updates, func(update RuntimeUpdate) bool {
		return update.Event == nil && update.State.Status == RuntimeStatusPending
	}, "queued initial pending update")

	cancelQueued()
	terminalUpdate := waitForRuntimeUpdate(t, updates, func(update RuntimeUpdate) bool {
		return update.State.Status == RuntimeStatusCanceled
	}, "queued canceled update")
	if terminalUpdate.State.Status != RuntimeStatusCanceled {
		t.Fatalf("queued state = %q, want canceled", terminalUpdate.State.Status)
	}
	waitForRuntimeUpdateClosed(t, updates, "queued canceled update")

	close(firstRelease)
	select {
	case <-queuedStarted:
		t.Fatal("queued runtime started after its submission context was canceled")
	case <-time.After(200 * time.Millisecond):
	}
	view, err := o.QueryRuntime(queuedPlan.RuntimeID)
	if err != nil {
		t.Fatalf("QueryRuntime(queued): %v", err)
	}
	if view.State.Status != RuntimeStatusCanceled {
		t.Fatalf("queued final state = %q, want canceled", view.State.Status)
	}
	cancelFirst()
}

func TestLoadRuntimeRecords_LoadsPersistedLog(t *testing.T) {
	o := newOrchestrator(testLogger)
	store := mustNewRuntimeStore(t, t.TempDir())
	if err := o.SetRuntimeStore(store); err != nil {
		t.Fatalf("SetRuntimeStore: %v", err)
	}
	plan, managedRuntime := mustPlanSingleNodeRuntime(t, o)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	sub := managedRuntime.Runtime.Subscribe(16)
	go func() {
		done <- managedRuntime.Runtime.Start(ctx)
	}()
	if err := managedRuntime.Runtime.AddNodes(ctx, managedRuntime.Nodes["only"]); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}
	waitForRuntimeEvent(t, sub, EventNodeCompleted, "only")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start err = %v, want context.Canceled", err)
	}

	records, err := o.LoadRuntimeRecords(context.Background(), plan.RuntimeID)
	if err != nil {
		t.Fatalf("LoadRuntimeRecords: %v", err)
	}
	if len(records) == 0 || records[0].Kind != RuntimeRecordInit {
		t.Fatalf("records = %+v, want init-prefixed log", records)
	}
	if !hasStoredEvent(records, EventNodeCompleted.String(), "only") {
		t.Fatal("missing persisted node-completed event")
	}
}

func TestRemoveRuntime_RejectsActiveRuntime(t *testing.T) {
	o := newOrchestrator(testLogger)
	store := mustNewRuntimeStore(t, t.TempDir())
	if err := o.SetRuntimeStore(store); err != nil {
		t.Fatalf("SetRuntimeStore: %v", err)
	}
	plan, managedRuntime := mustPlanSingleNodeRuntime(t, o)

	started := make(chan struct{})
	release := make(chan struct{})
	managedRuntime.Nodes["only"].Executor.RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(started)
		<-release
		return nil, nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- managedRuntime.Runtime.Start(ctx)
	}()
	if err := managedRuntime.Runtime.AddNodes(ctx, managedRuntime.Nodes["only"]); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}
	<-started

	if err := o.RemoveRuntime(context.Background(), plan.RuntimeID); !errors.Is(err, ErrRuntimeActive) {
		t.Fatalf("RemoveRuntime err = %v, want ErrRuntimeActive", err)
	}
	close(release)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start err = %v, want context.Canceled", err)
	}
}

func TestRemoveRuntime_DeletesTerminalRuntimeAndLog(t *testing.T) {
	o := newOrchestrator(testLogger)
	store := mustNewRuntimeStore(t, t.TempDir())
	if err := o.SetRuntimeStore(store); err != nil {
		t.Fatalf("SetRuntimeStore: %v", err)
	}
	plan, managedRuntime := mustPlanSingleNodeRuntime(t, o)
	managedRuntime.Nodes["only"].Executor.RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		return nil, nil, errors.New("boom")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- managedRuntime.Runtime.Start(ctx)
	}()
	if err := managedRuntime.Runtime.AddNodes(ctx, managedRuntime.Nodes["only"]); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}
	if err := <-done; err == nil {
		t.Fatal("expected runtime failure")
	}

	if err := o.RemoveRuntime(context.Background(), plan.RuntimeID); err != nil {
		t.Fatalf("RemoveRuntime: %v", err)
	}
	if _, err := o.Runtime(plan.RuntimeID); !errors.Is(err, ErrRuntimeNotFound) {
		t.Fatalf("Runtime err = %v, want ErrRuntimeNotFound", err)
	}
	if _, err := store.Load(context.Background(), plan.RuntimeID); !errors.Is(err, ErrRuntimeLogNotFound) {
		t.Fatalf("store.Load err = %v, want ErrRuntimeLogNotFound", err)
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
