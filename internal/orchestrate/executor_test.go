package orchestrate

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/llm/skill"
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

func TestNewExecutor_BindsSkillAndPlanner(t *testing.T) {
	sk := loadTestSkill(t)
	planner := &ExecutionPlanner{}
	exec, err := NewExecutor(nil, sk, planner)
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
	if _, err := NewExecutor(nil, nil, &ExecutionPlanner{}); err == nil {
		t.Fatal("expected error for nil skill")
	}
}

func TestNewExecutor_RejectsNilPlanner(t *testing.T) {
	if _, err := NewExecutor(nil, loadTestSkill(t), nil); err == nil {
		t.Fatal("expected error for nil planner")
	}
}

func TestPolishPlan_BumpsVersionAndFillsPlan(t *testing.T) {
	planner := &ExecutionPlanner{Version: 1}
	exec, err := NewExecutor(nil, loadTestSkill(t), planner)
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
	exec, err := NewExecutor(nil, loadTestSkill(t), &ExecutionPlanner{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	called := false
	exec.RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
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
	exec, err := NewExecutor(slog.New(slog.NewTextHandler(os.Stderr, nil)), loadTestSkill(t), &ExecutionPlanner{})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	out, nodes, err := exec.Execute(context.Background(), nil)
	if err != nil || out != nil || nodes != nil {
		t.Errorf("Execute = (%v, %v, %v), want (nil, nil, nil)", out, nodes, err)
	}
}
