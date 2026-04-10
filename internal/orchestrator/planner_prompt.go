package orchestrator

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

//go:embed prompts/planner_draft.md
var plannerDraftPrompt string

type plannerPromptInput struct {
	TaskID    string
	Request   string
	ToolsJSON string
}

type plannerCatalogEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	InputTypes  []string `json:"input_types"`
	OutputTypes []string `json:"output_types"`
	Model       string   `json:"model,omitempty"`
}

func renderPlannerDraftPrompt(taskID string, request string, tools []catalogTool) (string, error) {
	entries := make([]plannerCatalogEntry, 0, len(tools))
	for _, tool := range tools {
		entries = append(entries, plannerCatalogEntry{
			Name:        tool.Spec.Name,
			Description: tool.Spec.Description,
			InputTypes:  append([]string(nil), tool.Spec.InputTypes...),
			OutputTypes: append([]string(nil), tool.Spec.OutputTypes...),
			Model:       tool.Spec.Model,
		})
	}
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
	if err := tmpl.Execute(&out, plannerPromptInput{
		TaskID:    taskID,
		Request:   strings.TrimSpace(request),
		ToolsJSON: string(payload),
	}); err != nil {
		return "", fmt.Errorf("render planner prompt: %w", err)
	}
	return out.String(), nil
}
