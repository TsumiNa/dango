package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/terrai/skillframe/llm"
	"github.com/terrai/skillframe/skill"
)

// LLMMatcher uses an LLM to score skill relevance.
// More accurate than keyword matching but has latency cost.
// Best used with a small, fast model for routing.
type LLMMatcher struct {
	Client llm.Client
	Model  string // routing model (e.g. "claude-haiku-4-5-20251001")
}

type llmMatchResult struct {
	Scores map[string]float64 `json:"scores"`
}

// ScoreBatch scores all skills at once (single LLM call).
// Returns a map of skill name -> score.
func (m *LLMMatcher) ScoreBatch(query string, skills []*skill.Skill) (map[string]float64, error) {
	var catalog strings.Builder
	for _, s := range skills {
		catalog.WriteString(fmt.Sprintf("- name: %s\n  description: %s\n", s.Name, s.Description))
	}

	prompt := fmt.Sprintf(`Given this user query and skill catalog, score each skill's relevance from 0.0 to 1.0.
Return ONLY a JSON object like {"scores": {"skill-name": 0.8, ...}}

Query: %s

Skills:
%s`, query, catalog.String())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := m.Client.Complete(ctx, &llm.Request{
		Model:     m.Model,
		MaxTokens: 256,
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("llm score: %w", err)
	}

	// Parse JSON from response
	content := strings.TrimSpace(resp.Content)
	// Strip markdown code fences if present
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var result llmMatchResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("parse scores: %w (raw: %s)", err, content)
	}

	return result.Scores, nil
}

// Score implements the Matcher interface (per-skill, less efficient).
// Prefer ScoreBatch when possible.
func (m *LLMMatcher) Score(query string, s *skill.Skill) float64 {
	scores, err := m.ScoreBatch(query, []*skill.Skill{s})
	if err != nil {
		return 0
	}
	return scores[s.Name]
}
