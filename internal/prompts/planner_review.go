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
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/taskflow"
)

//go:embed planner_review.md
var plannerReviewPrompt string

//go:embed planner_repair.md
var plannerRepairPrompt string

type plannerReviewInput struct {
	TaskID      string
	RequestJSON string
	ToolsJSON   string
	PlanJSON    string
	Reason      string
}

// RenderPlannerReview renders the built-in runner plan review prompt.
func RenderPlannerReview(taskID string, request taskflow.RequestEnvelope, tools []llm.ToolCatalogEntry, plan spec.DAGPlan) (string, error) {
	return renderPlannerReviewPrompt("planner_review", plannerReviewPrompt, plannerReviewInput{
		TaskID:      taskID,
		RequestJSON: mustJSON(request),
		ToolsJSON:   mustSortedToolsJSON(tools),
		PlanJSON:    mustJSON(plan),
	})
}

// RenderPlannerRepair renders the built-in runner plan repair prompt.
func RenderPlannerRepair(taskID string, request taskflow.RequestEnvelope, tools []llm.ToolCatalogEntry, plan spec.DAGPlan, reason string) (string, error) {
	return renderPlannerReviewPrompt("planner_repair", plannerRepairPrompt, plannerReviewInput{
		TaskID:      taskID,
		RequestJSON: mustJSON(request),
		ToolsJSON:   mustSortedToolsJSON(tools),
		PlanJSON:    mustJSON(plan),
		Reason:      strings.TrimSpace(reason),
	})
}

func renderPlannerReviewPrompt(name string, source string, input plannerReviewInput) (string, error) {
	tmpl, err := template.New(name).Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse %s prompt template: %w", name, err)
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, input); err != nil {
		return "", fmt.Errorf("render %s prompt: %w", name, err)
	}
	return strings.TrimSpace(out.String()), nil
}

func mustJSON(value any) string {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func mustSortedToolsJSON(tools []llm.ToolCatalogEntry) string {
	entries := append([]llm.ToolCatalogEntry(nil), tools...)
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return mustJSON(entries)
}
