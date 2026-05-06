package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/tsumina/dango/internal/llm"
)

type stubRegistry struct {
	skills map[string]*llm.Skill
}

func (s *stubRegistry) Skills() map[string]*llm.Skill {
	out := make(map[string]*llm.Skill, len(s.skills))
	for k, v := range s.skills {
		out[k] = v
	}
	return out
}

func TestListSkillsToolReturnsSortedJSON(t *testing.T) {
	registry := &stubRegistry{skills: map[string]*llm.Skill{
		"zeta":     {Name: "zeta", Description: "Last skill alphabetically."},
		"alpha":    {Name: "alpha", Description: "First skill alphabetically."},
		"midrange": {Name: "midrange", Description: "Middle skill."},
	}}
	tool := newListSkillsTool(registry)
	out, err := tool.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	alpha := strings.Index(out, `"name": "alpha"`)
	mid := strings.Index(out, `"name": "midrange"`)
	zeta := strings.Index(out, `"name": "zeta"`)
	if alpha < 0 || mid < 0 || zeta < 0 {
		t.Fatalf("expected all skills present in output:\n%s", out)
	}
	if !(alpha < mid && mid < zeta) {
		t.Fatalf("expected alphabetical order, got order: alpha=%d mid=%d zeta=%d\n%s", alpha, mid, zeta, out)
	}
	for _, want := range []string{`"description": "First skill alphabetically."`, `"description": "Last skill alphabetically."`} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestOrchestratorRegistryToolsExposeOnlyPublicSurface(t *testing.T) {
	registry := &stubRegistry{skills: map[string]*llm.Skill{
		"keep_private": {Name: "keep_private", Description: "Public summary.", Instruction: "Internal SKILL.md body that or must not see."},
	}}
	tools := orchestratorRegistryTools(registry)
	for _, tool := range tools {
		switch tool.Name() {
		case "list_skills":
			// allowed
		default:
			t.Fatalf("orchestrator must not expose detailed introspection tool %q (privacy boundary protects executor SKILL.md bodies)", tool.Name())
		}
	}
	out, err := tools[0].Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(out, "Internal SKILL.md body") {
		t.Fatalf("list_skills leaked instruction body:\n%s", out)
	}
}

func TestEmbeddedOrchestratorSkillCarriesRegistryAndWorkspaceTools(t *testing.T) {
	registry := &stubRegistry{skills: map[string]*llm.Skill{
		"only_one": {Name: "only_one", Description: "Single registered skill."},
	}}
	sk, err := loadOrchestratorSkill(orchestratorRegistryTools(registry)...)
	if err != nil {
		t.Fatalf("loadOrchestratorSkill: %v", err)
	}
	bound, err := sk.Bind(&llm.Client{}, llm.DefaultConversationConfig())
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	names := map[string]bool{}
	for _, spec := range bound.Conversation().Tools() {
		names[spec.Name] = true
	}
	for _, want := range []string{"list_skills", "bash", "read_file", "write_file", "list_dir", "grep", "pwd"} {
		if !names[want] {
			t.Fatalf("orchestrator skill missing tool %q (have %v)", want, names)
		}
	}
	if names["describe_skill"] {
		t.Fatal("orchestrator must not expose describe_skill — executor SKILL.md is private to the executor")
	}
}

func TestNewOrchestratorSkillBindsRegistryTools(t *testing.T) {
	o := NewOrchestrator()
	bound, err := o.OrchestratorSkill().Bind(&llm.Client{}, llm.DefaultConversationConfig())
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	names := map[string]bool{}
	for _, spec := range bound.Conversation().Tools() {
		names[spec.Name] = true
	}
	if !names["list_skills"] {
		t.Fatalf("orchestrator skill missing list_skills (have %v)", names)
	}
}
