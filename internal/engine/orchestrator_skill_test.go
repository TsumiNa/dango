package engine

import (
	"testing"

	"github.com/tsumina/dango/internal/llm"
)

func TestDefaultOrchestratorSkill_LoadsEmbeddedSkillDirectory(t *testing.T) {
	sk := defaultOrchestratorSkill()
	if sk == nil {
		t.Fatal("defaultOrchestratorSkill() = nil, want embedded skill")
	}
	if sk.Name != "orchestrator" {
		t.Fatalf("Name = %q, want %q", sk.Name, "orchestrator")
	}
	if sk.Description == "" {
		t.Fatal("Description = empty, want embedded metadata")
	}
	if sk.Instruction == "" {
		t.Fatal("Instruction = empty, want embedded SKILL.md body")
	}
	if sk.Dir() == nil {
		t.Fatal("Dir() = nil, want embedded skill filesystem")
	}
	if sk.Client() != nil {
		t.Fatalf("Client() = %p, want nil for lightweight embedded skill", sk.Client())
	}
	if sk.Conversation() != nil {
		t.Fatal("Conversation() should be nil for lightweight embedded skill")
	}
	if _, err := NewEmbeddedOrchestratorSkill(&llm.Client{}, nil, nil); err != nil {
		t.Fatalf("NewEmbeddedOrchestratorSkill: %v", err)
	}
}
