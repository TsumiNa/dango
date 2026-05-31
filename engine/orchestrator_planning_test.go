package engine

import (
	"testing"

	"github.com/tsumina/dango/llm"
)

func TestCollectSkillSummaries_SortsByName(t *testing.T) {
	summaries := collectSkillSummaries(map[string]*llm.Skill{
		"zeta":  {Description: "last"},
		"alpha": {Description: "first"},
		"mid":   {Description: "middle"},
	})

	if len(summaries) != 3 {
		t.Fatalf("len(summaries) = %d, want 3", len(summaries))
	}
	if summaries[0].Name != "alpha" || summaries[1].Name != "mid" || summaries[2].Name != "zeta" {
		t.Fatalf("summary order = %#v, want alpha, mid, zeta", summaries)
	}
	if summaries[0].Description != "first" || summaries[1].Description != "middle" || summaries[2].Description != "last" {
		t.Fatalf("summary descriptions = %#v, want preserved descriptions", summaries)
	}
}

func TestRuntimeOrchestratorSkill_ClonesInitializedSkillClient(t *testing.T) {
	initializedClient := &llm.Client{}
	envClient := &llm.Client{}
	sk, err := defaultOrchestratorSkill().Bind(initializedClient, llm.DefaultConversationConfig())
	if err != nil {
		t.Fatalf("Bind(default orchestrator skill): %v", err)
	}
	runtimeSkill, err := runtimeOrchestrator(sk, envClient, nil, llm.ConversationConfig{})
	if err != nil {
		t.Fatalf("runtimeOrchestratorSkill: %v", err)
	}
	if runtimeSkill == sk {
		t.Fatalf("runtimeOrchestratorSkill() reused startup skill %p, want independent runtime copy", sk)
	}
	if runtimeSkill.Client() != initializedClient {
		t.Fatalf("runtime skill client = %p, want initialized skill client %p", runtimeSkill.Client(), initializedClient)
	}
	if runtimeSkill.Conversation() == nil {
		t.Fatal("runtime skill conversation = nil, want bound runtime conversation")
	}
	if runtimeSkill.Conversation() == sk.Conversation() {
		t.Fatal("runtime skill reused startup conversation, want independent conversation")
	}
}
