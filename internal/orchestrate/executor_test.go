package orchestrate

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/llm/skill"
	runnerpkg "github.com/tsumina/dango/internal/orchestrate/runner"
)

func loadTestSkill(t *testing.T) *skill.Skill {
	t.Helper()
	dir := t.TempDir()
	content := "---\nname: t\ndescription: d\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, skill.SkillFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	sk, err := skill.New(skill.Config{Dir: dir, Client: &llm.Client{}})
	if err != nil {
		t.Fatalf("skill.New: %v", err)
	}
	return sk
}

func loadLightweightTestSkill(t *testing.T) *skill.Skill {
	t.Helper()
	dir := t.TempDir()
	content := "---\nname: t\ndescription: d\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, skill.SkillFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	sk, err := skill.Load(dir)
	if err != nil {
		t.Fatalf("skill.Load: %v", err)
	}
	return sk
}

func TestNewExecutor_BindsSkillAndPlanner(t *testing.T) {
	sk := loadTestSkill(t)
	planner := &ExecutionPlanner{}
	exec, err := NewExecutor(nil, sk, planner, nil, nil)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if exec.Skill() != sk {
		t.Errorf("Skill() = %p, want %p", exec.Skill(), sk)
	}
	if exec.Planner() != planner {
		t.Errorf("Planner() = %p, want %p", exec.Planner(), planner)
	}
}

func TestNewExecutor_RejectsNilSkill(t *testing.T) {
	if _, err := NewExecutor(nil, nil, &ExecutionPlanner{}, nil, nil); err == nil {
		t.Fatal("expected error for nil skill")
	}
}

func TestNewExecutor_RejectsNilPlanner(t *testing.T) {
	if _, err := NewExecutor(nil, loadTestSkill(t), nil, nil, nil); err == nil {
		t.Fatal("expected error for nil planner")
	}
}

func TestPolishPlan_BumpsVersionAndFillsPlan(t *testing.T) {
	planner := &ExecutionPlanner{Version: 1}
	exec, err := NewExecutor(nil, loadTestSkill(t), planner, nil, nil)
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
	exec, err := NewExecutor(nil, loadTestSkill(t), &ExecutionPlanner{}, nil, nil)
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
	exec, err := NewExecutor(slog.New(slog.NewTextHandler(os.Stderr, nil)), loadTestSkill(t), &ExecutionPlanner{}, nil, nil)
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
	exec, err := NewExecutor(nil, loadTestSkill(t), &ExecutionPlanner{}, nil, nil)
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

func TestLLMClient_PrefersSkillClientWhenNoHigherPrioritySourceExists(t *testing.T) {
	clearLLMEnv(t)
	skillClient := &llm.Client{}
	fallbackClient := &llm.Client{}
	sk := loadTestSkill(t)
	bound, err := sk.Bind(skill.RuntimeConfig{Client: skillClient})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	exec, err := NewExecutor(nil, bound, &ExecutionPlanner{}, fallbackClient, nil)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if got := exec.LLMClient(); got != skillClient {
		t.Fatalf("LLMClient() = %p, want skill client %p", got, skillClient)
	}
}

func TestRuntimeSkill_BindsLightweightSkillWithFallbackClient(t *testing.T) {
	clearLLMEnv(t)
	fallbackClient := &llm.Client{}
	lightweight := loadLightweightTestSkill(t)
	exec, err := NewExecutor(nil, lightweight, &ExecutionPlanner{}, fallbackClient, nil)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	runtimeSkill, err := exec.runtimeSkill()
	if err != nil {
		t.Fatalf("runtimeSkill: %v", err)
	}
	if runtimeSkill == lightweight {
		t.Fatal("runtimeSkill returned the original lightweight skill, want bound copy")
	}
	if runtimeSkill.Client() != fallbackClient {
		t.Fatalf("runtimeSkill client = %p, want %p", runtimeSkill.Client(), fallbackClient)
	}
	if runtimeSkill.Conversation() == nil {
		t.Fatal("runtimeSkill conversation = nil, want runnable conversation")
	}
	if lightweight.Client() != nil {
		t.Fatal("lightweight skill unexpectedly gained a client")
	}
}

func TestLLMClient_PrefersEnvOverSkillAndFallback(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	t.Setenv("ORCHESTRATION_MODEL", "env-model")
	sk := loadTestSkill(t)
	fallbackClient := &llm.Client{}
	exec, err := NewExecutor(nil, sk, &ExecutionPlanner{}, fallbackClient, nil)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	client := exec.LLMClient()
	if client == nil {
		t.Fatal("LLMClient() = nil, want env client")
	}
	if client == sk.Client() {
		t.Fatal("LLMClient() used the skill client, want env client")
	}
	if client == fallbackClient {
		t.Fatal("LLMClient() used the fallback client, want env client")
	}
	if client.Provider() != llm.ProviderOpenAI {
		t.Fatalf("Provider() = %q, want %q", client.Provider(), llm.ProviderOpenAI)
	}
	if client.Model() != "env-model" {
		t.Fatalf("Model() = %q, want %q", client.Model(), "env-model")
	}
}

func TestRuntimeSkill_PrefersSkillFactoryOverOrchestratorFallback(t *testing.T) {
	clearLLMEnv(t)
	perSkillClient := &llm.Client{}
	fallbackClient := &llm.Client{}
	lightweight := loadLightweightTestSkill(t)
	exec, err := NewExecutor(nil, lightweight, &ExecutionPlanner{}, fallbackClient, func() (*llm.Client, error) {
		return perSkillClient, nil
	})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	runtimeSkill, err := exec.runtimeSkill()
	if err != nil {
		t.Fatalf("runtimeSkill: %v", err)
	}
	if runtimeSkill.Client() != perSkillClient {
		t.Fatalf("runtimeSkill client = %p, want %p", runtimeSkill.Client(), perSkillClient)
	}
}
