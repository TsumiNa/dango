package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	"github.com/tsumina/dango/internal/llm"
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

func newOrchestrator(logger *slog.Logger) *Orchestrator {
	return NewOrchestrator(context.Background(), logger)
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
		"MODEL",
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
	if err := os.WriteFile(filepath.Join(dir, llm.SkillFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return dir
}

func loadTestSkillFromDir(t *testing.T, dir string) *llm.Skill {
	t.Helper()
	sk, err := llm.NewSkill(dir, nil, nil)
	if err != nil {
		t.Fatalf("llm.New(%q): %v", dir, err)
	}
	return sk
}

func newTestSkillConfig(t *testing.T, name, description string, client *llm.Client) AddSkillConfig {
	t.Helper()
	if client == nil {
		client = &llm.Client{}
	}
	return AddSkillConfig{
		Skill:  loadTestSkillFromDir(t, writeTestSkill(t, name, description)),
		Client: client,
	}
}

func mustAddSkills(t *testing.T, o *Orchestrator, cfgs ...AddSkillConfig) {
	t.Helper()
	if err := o.AddSkills(cfgs...); err != nil {
		t.Fatalf("AddSkills: %v", err)
	}
}

func bindTestOrchestratorSkill(t *testing.T, outputs ...string) *llm.Skill {
	t.Helper()
	clearLLMEnv(t)
	var responded int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		text := outputs[len(outputs)-1]
		if responded < len(outputs) {
			text = outputs[responded]
		}
		responded++
		payload, err := json.Marshal(map[string]any{
			"id":         "r1",
			"object":     "response",
			"created_at": 0,
			"model":      "test-model",
			"status":     "completed",
			"output": []map[string]any{{
				"id":     "m1",
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]any{{
					"type":        "output_text",
					"text":        text,
					"annotations": []any{},
				}},
			}},
			"parallel_tool_calls": false,
			"tool_choice":         "auto",
			"tools":               []any{},
		})
		if err != nil {
			t.Fatalf("marshal planner response: %v", err)
		}
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	raw := openai.NewClient(option.WithAPIKey("test-key"), option.WithBaseURL(srv.URL+"/"))
	client, err := llm.NewClient(llm.ClientConfig{Provider: llm.ProviderOpenAI, Model: "test-model", Raw: raw})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	bound, err := defaultOrchestratorSkill().Bind(client, nil, nil)
	if err != nil {
		t.Fatalf("Bind(default orchestrator skill): %v", err)
	}
	return bound
}

func mustPlanJSON(t *testing.T, plan *CoarsePlan) string {
	t.Helper()
	buf, err := json.Marshal(map[string]any{"plan": plan})
	if err != nil {
		t.Fatalf("marshal plan json: %v", err)
	}
	return string(buf)
}

func mustRejectJSON(t *testing.T, reject *RejectReason) string {
	t.Helper()
	buf, err := json.Marshal(map[string]any{"reject": reject})
	if err != nil {
		t.Fatalf("marshal reject json: %v", err)
	}
	return string(buf)
}

func mustReviewJSON(t *testing.T, approved bool, reason string) string {
	t.Helper()
	buf, err := json.Marshal(map[string]any{"approved": approved, "reason": reason})
	if err != nil {
		t.Fatalf("marshal review json: %v", err)
	}
	return string(buf)
}

func mustPlanSingleNodeRunner(t *testing.T, o *Orchestrator) (*CoarsePlan, *runnerpkg.Runner) {
	t.Helper()
	return mustPlanSingleNodeRunnerWithOutputs(t, o, mustPlanJSON(t, &CoarsePlan{
		Request: "run a single node",
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node.",
		}},
	}))
}

func mustPlanSingleNodeRunnerWithOutputs(t *testing.T, o *Orchestrator, outputs ...string) (*CoarsePlan, *runnerpkg.Runner) {
	t.Helper()
	mustAddSkills(t, o, newTestSkillConfig(t, "single", "Single-step runner.", nil))
	if len(outputs) == 0 {
		outputs = []string{mustPlanJSON(t, &CoarsePlan{
			Request: "run a single node",
			Nodes: []CoarsePlanNode{{
				ID:              "only",
				SkillName:       "single",
				TaskDescription: "Run the only node.",
			}},
		}), mustReviewJSON(t, true, "")}
	} else if len(outputs) == 1 {
		outputs = append(outputs, mustReviewJSON(t, true, ""))
	}
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, outputs...)); err != nil {
		t.Fatalf("SetOrchestratorSkill: %v", err)
	}
	runnerID, err := o.StartRequest(context.Background(), &Request{Input: "run a single node"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	managedRunner, ok := o.Runners()[runnerID]
	if !ok || managedRunner == nil {
		t.Fatalf("expected runner %q to be stored", runnerID)
	}
	plan := managedRunner.Plan()
	if plan == nil {
		t.Fatal("expected runner plan to be populated")
	}
	return plan, managedRunner
}

func mustRejectStartRequest(t *testing.T, o *Orchestrator) *RejectReason {
	t.Helper()
	_, err := o.StartRequest(context.Background(), &Request{Input: "summarize this repository"})
	var rejected *RequestRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("StartRequest rejection err = %v, want RequestRejectedError", err)
	}
	if rejected.Reason == nil {
		t.Fatal("RequestRejectedError.Reason = nil")
	}
	return rejected.Reason
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
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for runner update stream to close: %s", label)
		}
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
