package engine

import (
	"testing"

	"github.com/tsumina/dango/internal/llm"
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

func TestRuntimeOrchestratorSkill_PrefersInitializedSkillClient(t *testing.T) {
	initializedClient := &llm.Client{}
	envClient := &llm.Client{}
	sk, err := defaultOrchestratorSkill().Bind(initializedClient, nil, nil)
	if err != nil {
		t.Fatalf("Bind(default orchestrator skill): %v", err)
	}
	runtimeSkill, err := runtimeOrchestratorSkill(sk, envClient, nil)
	if err != nil {
		t.Fatalf("runtimeOrchestratorSkill: %v", err)
	}
	if runtimeSkill != sk {
		t.Fatalf("runtimeOrchestratorSkill() = %p, want same initialized skill %p", runtimeSkill, sk)
	}
	if runtimeSkill.Client() != initializedClient {
		t.Fatalf("runtime skill client = %p, want initialized skill client %p", runtimeSkill.Client(), initializedClient)
	}
}
