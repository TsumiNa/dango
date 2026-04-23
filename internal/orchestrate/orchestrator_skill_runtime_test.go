package orchestrate

import (
	"testing"

	"github.com/tsumina/dango/internal/llm/skill"
)

func TestCollectSkillSummaries_SortsByName(t *testing.T) {
	summaries := collectSkillSummaries(map[string]*skill.Skill{
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