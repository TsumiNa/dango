package engine

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	"github.com/tsumina/dango/internal/llm"
)

func loadTestSkill(t *testing.T) *llm.Skill {
	t.Helper()
	dir := t.TempDir()
	content := "---\nname: t\ndescription: d\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, llm.SkillFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	loaded, err := llm.NewSkill(dir, nil, nil)
	if err != nil {
		t.Fatalf("llm.New: %v", err)
	}
	sk, err := loaded.Bind(&llm.Client{}, nil, nil)
	if err != nil {
		t.Fatalf("Skill.Bind: %v", err)
	}
	return sk
}

func loadLightweightTestSkill(t *testing.T) *llm.Skill {
	t.Helper()
	dir := t.TempDir()
	content := "---\nname: t\ndescription: d\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, llm.SkillFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	sk, err := llm.NewSkill(dir, nil, nil)
	if err != nil {
		t.Fatalf("llm.New: %v", err)
	}
	return sk
}

func TestNewExecutor_BindsSkillAndPlanner(t *testing.T) {
	sk := loadLightweightTestSkill(t)
	client := &llm.Client{}
	planner := &ExecutionPlanner{}
	exec, err := NewExecutor(nil, sk, client, nil, planner)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if exec.Skill() != sk {
		t.Errorf("Skill() = %p, want %p", exec.Skill(), sk)
	}
	if exec.Planner() != planner {
		t.Errorf("Planner() = %p, want %p", exec.Planner(), planner)
	}
	if exec.LLMClient() != client {
		t.Fatalf("LLMClient() = %p, want %p", exec.LLMClient(), client)
	}
}

func TestNewExecutor_RejectsNilSkill(t *testing.T) {
	if _, err := NewExecutor(nil, nil, nil, nil, &ExecutionPlanner{}); err == nil {
		t.Fatal("expected error for nil skill")
	}
}

func TestNewExecutor_RejectsNilPlanner(t *testing.T) {
	if _, err := NewExecutor(nil, loadLightweightTestSkill(t), nil, nil, nil); err == nil {
		t.Fatal("expected error for nil planner")
	}
}

func TestPolishPlan_BumpsVersionAndFillsPlan(t *testing.T) {
	planner := &ExecutionPlanner{Version: 1}
	exec, err := NewExecutor(nil, loadLightweightTestSkill(t), &llm.Client{}, nil, planner)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if err := exec.PolishPlan(); err != nil {
		t.Fatalf("PolishPlan: %v", err)
	}
	if planner.Version != 2 {
		t.Errorf("Version = %d, want 2", planner.Version)
	}
	if planner.Reason == "" || planner.Solution == "" {
		t.Errorf("expected planner Reason and Solution to be set, got %+v", planner)
	}
}

func TestExecute_UsesRunEWhenSet(t *testing.T) {
	exec, err := NewExecutor(nil, loadLightweightTestSkill(t), &llm.Client{}, nil, &ExecutionPlanner{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	called := false
	exec.RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*runnerpkg.Node, error) {
		called = true
		return "ok", nil, nil
	}
	out, nodes, err := exec.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !called {
		t.Error("expected RunE to be invoked")
	}
	if out != "ok" || nodes != nil {
		t.Errorf("Execute returned (%v, %v), want (\"ok\", nil)", out, nodes)
	}
}

func TestExecute_NoRunEReturnsZero(t *testing.T) {
	exec, err := NewExecutor(slog.New(slog.NewTextHandler(os.Stderr, nil)), loadLightweightTestSkill(t), &llm.Client{}, nil, &ExecutionPlanner{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	out, nodes, err := exec.Execute(context.Background(), nil)
	if err != nil || out != nil || nodes != nil {
		t.Errorf("Execute = (%v, %v, %v), want (nil, nil, nil)", out, nodes, err)
	}
}

func TestExecute_RespectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exec, err := NewExecutor(nil, loadLightweightTestSkill(t), &llm.Client{}, nil, &ExecutionPlanner{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, _, err := exec.Execute(ctx, nil); err == nil {
		t.Fatal("expected Execute to return ctx.Err for canceled context")
	}
	if exec.Status != StatusFailed {
		t.Fatalf("Status = %v, want StatusFailed", exec.Status)
	}
}

func TestLLMClient_ReturnsConfiguredClientBeforeRunnerBind(t *testing.T) {
	client := &llm.Client{}
	exec, err := NewExecutor(nil, loadLightweightTestSkill(t), client, nil, &ExecutionPlanner{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if got := exec.LLMClient(); got != client {
		t.Fatalf("LLMClient() = %p, want %p", got, client)
	}
}

func TestRuntimeSkill_RequiresRunnerBinding(t *testing.T) {
	lightweight := loadLightweightTestSkill(t)
	exec, err := NewExecutor(nil, lightweight, &llm.Client{}, nil, &ExecutionPlanner{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, err := exec.runtimeSkill(); err == nil {
		t.Fatal("expected runtimeSkill to fail before runner binding")
	}
}

func TestBindForRunner_BindsLightweightSkillAndAllocatesSession(t *testing.T) {
	client := &llm.Client{}
	store, err := llm.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	lightweight := loadLightweightTestSkill(t)
	exec, err := NewExecutor(nil, lightweight, client, nil, &ExecutionPlanner{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	sessionID, err := exec.BindForRunner(nil, store)
	if err != nil {
		t.Fatalf("BindForRunner: %v", err)
	}
	if sessionID == "" {
		t.Fatal("BindForRunner returned an empty session id")
	}
	runtimeSkill, err := exec.runtimeSkill()
	if err != nil {
		t.Fatalf("runtimeSkill: %v", err)
	}
	if runtimeSkill == lightweight {
		t.Fatal("runtimeSkill returned the original lightweight skill, want bound copy")
	}
	if runtimeSkill.Client() != client {
		t.Fatalf("runtimeSkill client = %p, want %p", runtimeSkill.Client(), client)
	}
	if conv := runtimeSkill.Conversation(); conv == nil || conv.SessionID() != sessionID {
		t.Fatalf("conversation/session = %v, want session %q", conv, sessionID)
	}
}

func TestBindForRunner_ReusesExistingSession(t *testing.T) {
	client := &llm.Client{}
	store, err := llm.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	exec, err := NewExecutor(nil, loadLightweightTestSkill(t), client, nil, &ExecutionPlanner{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	firstSessionID, err := exec.BindForRunner(nil, store)
	if err != nil {
		t.Fatalf("BindForRunner(first): %v", err)
	}
	secondSessionID, err := exec.BindForRunner(&firstSessionID, store)
	if err != nil {
		t.Fatalf("BindForRunner(second): %v", err)
	}
	if secondSessionID != firstSessionID {
		t.Fatalf("session id = %q, want %q", secondSessionID, firstSessionID)
	}
}
