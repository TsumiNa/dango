package prompts

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/tsumina/dango/internal/llm"
)

//go:embed planner_draft.md
var plannerDraftPrompt string

type plannerDraftInput struct {
	TaskID    string
	Request   string
	ToolsJSON string
}

// RenderPlannerDraft renders the built-in runner draft-planning prompt.
func RenderPlannerDraft(taskID string, request string, tools []llm.ToolCatalogEntry) (string, error) {
	entries := append([]llm.ToolCatalogEntry(nil), tools...)
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	payload, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal planner tool catalog: %w", err)
	}

	tmpl, err := template.New("planner_draft").Parse(plannerDraftPrompt)
	if err != nil {
		return "", fmt.Errorf("parse planner prompt template: %w", err)
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, plannerDraftInput{
		TaskID:    taskID,
		Request:   strings.TrimSpace(request),
		ToolsJSON: string(payload),
	}); err != nil {
		return "", fmt.Errorf("render planner prompt: %w", err)
	}
	return out.String(), nil
}
