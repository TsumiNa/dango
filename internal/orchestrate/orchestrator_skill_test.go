package orchestrate

import (
	"testing"

	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/llm/skill"
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
	if sk.Dir() != "" {
		t.Fatalf("Dir() = %q, want empty for embedded skill", sk.Dir())
	}
	if sk.Client() != nil {
		t.Fatalf("Client() = %p, want nil for lightweight embedded skill", sk.Client())
	}
	if sk.Conversation() != nil {
		t.Fatal("Conversation() should be nil for lightweight embedded skill")
	}
	if _, err := NewEmbeddedOrchestratorSkill(skill.RuntimeConfig{Client: &llm.Client{}}); err != nil {
		t.Fatalf("NewEmbeddedOrchestratorSkill: %v", err)
	}
}