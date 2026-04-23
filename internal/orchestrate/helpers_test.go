package orchestrate

import (
	"context"
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

var testLogger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

type Node = runnerpkg.Node
type EventType = runnerpkg.EventType
type RunnerEvent = runnerpkg.RunnerEvent
type RunnerRecord = runnerpkg.RunnerRecord
type RunnerView = runnerpkg.RunnerView
type RunnerUpdate = runnerpkg.RunnerUpdate
type RunnerPhase = runnerpkg.RunnerPhase

const (
	EventNodeAdded       = runnerpkg.EventNodeAdded
	EventNodeStarted     = runnerpkg.EventNodeStarted
	EventNodeCompleted   = runnerpkg.EventNodeCompleted
	EventNodeFailed      = runnerpkg.EventNodeFailed
	EventEngineIdle      = runnerpkg.EventEngineIdle
	EventEngineStopped   = runnerpkg.EventEngineStopped
	RunnerStatusPending  = runnerpkg.RunnerStatusPending
	RunnerStatusRunning  = runnerpkg.RunnerStatusRunning
	RunnerStatusIdle     = runnerpkg.RunnerStatusIdle
	RunnerStatusFailed   = runnerpkg.RunnerStatusFailed
	RunnerStatusCanceled = runnerpkg.RunnerStatusCanceled
	RunnerRecordInit     = runnerpkg.RunnerRecordInit
	PhaseCreated         = runnerpkg.PhaseCreated
	PhasePolishing       = runnerpkg.PhasePolishing
	PhaseAwaitingReview  = runnerpkg.PhaseAwaitingReview
	PhaseAwaitingReplan  = runnerpkg.PhaseAwaitingReplan
	PhaseExecuting       = runnerpkg.PhaseExecuting
	PhaseReport          = runnerpkg.PhaseReport
	PhaseSettled         = runnerpkg.PhaseSettled
)

var ErrRunnerLogNotFound = runnerpkg.ErrRunnerLogNotFound

func resetDefaultOrchestrator(t *testing.T) {
	t.Helper()
	defaultOrchestrator = nil
	defaultOrchestratorOnce = sync.Once{}
}

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func clearLLMEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"OPENAI_API_KEY",
		"OPENROUTER_API_KEY",
		"GEMINI_API_KEY",
		"ORCHESTRATION_MODEL",
		"REASONING_EFFORT",
		"REASONING_REPLAY",
	} {
		t.Setenv(key, "")
	}
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

func mustNewRunnerStore(t *testing.T, dir string) *runnerpkg.JSONRunnerStore {
	t.Helper()
	store, err := runnerpkg.NewJSONRunnerStore(dir)
	if err != nil {
		t.Fatalf("NewJSONRunnerStore: %v", err)
	}
	return store
}

func waitForRunnerEvent(t *testing.T, ch <-chan runnerpkg.RunnerEvent, want runnerpkg.EventType, nodeID string) runnerpkg.RunnerEvent {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatalf("timed out waiting for event %s/%s", want.String(), nodeID)
		case ev := <-ch:
			if ev.Type == want && ev.NodeID == nodeID {
				return ev
			}
		}
	}
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

func hasStoredEvent(records []runnerpkg.RunnerRecord, eventType string, nodeID string) bool {
	for _, rec := range records {
		if rec.Kind != runnerpkg.RunnerRecordEvent || rec.Event == nil {
			continue
		}
		if rec.Event.Type == eventType && rec.Event.NodeID == nodeID {
			return true
		}
	}
	return false
}

func mustNodeExecutor(t *testing.T, node *runnerpkg.Node) *Executor {
	t.Helper()
	executor, ok := node.Executor.(*Executor)
	if !ok || executor == nil {
		t.Fatalf("node %q executor = %T, want *Executor", node.Id, node.Executor)
	}
	return executor
}

type stubRunnerExecutor struct {
	polish  func(ctx context.Context) (any, error)
	execute func(ctx context.Context, parentOutputs map[string]any) (any, []*runnerpkg.Node, error)
	report  func(ctx context.Context, output any) (any, error)
}

func (e *stubRunnerExecutor) Polish(ctx context.Context) (any, error) {
	if e.polish == nil {
		return nil, nil
	}
	return e.polish(ctx)
}

func (e *stubRunnerExecutor) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*runnerpkg.Node, error) {
	if e.execute == nil {
		return nil, nil, nil
	}
	return e.execute(ctx, parentOutputs)
}

func (e *stubRunnerExecutor) Report(ctx context.Context, output any) (any, error) {
	if e.report == nil {
		return output, nil
	}
	return e.report(ctx, output)
}
