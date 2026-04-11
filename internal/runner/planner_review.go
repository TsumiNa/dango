package runner

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/ai"
	"github.com/tsumina/dango/internal/spec"
)

type plannerReviewResponse struct {
	Approved bool                    `json:"approved"`
	Plan     *plannerReviewPlanPatch `json:"plan,omitempty"`
}

type plannerReviewPlanPatch struct {
	Mode  string             `json:"mode,omitempty"`
	Edges []spec.PlannedEdge `json:"edges,omitempty"`
}

func normalizeReviewedPlanResponse(payload []byte, original spec.DAGPlan, tools []ai.ToolCatalogEntry, kind ai.Kind) (spec.DAGPlan, error) {
	var response plannerReviewResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return spec.DAGPlan{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			kind,
			"planning review returned invalid JSON",
			err,
		)
	}

	if response.Plan == nil {
		if response.Approved {
			original.ReviewedAt = time.Now().UTC()
			if original.Mode == "" {
				original.Mode = normalizePlanMode(original.Mode, original.Edges)
			}
			return original, nil
		}
		return spec.DAGPlan{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			kind,
			"planning review rejected the plan without returning a corrected plan",
			nil,
		)
	}

	reviewed, err := validateReviewedPlan(*response.Plan, original, tools)
	if err != nil {
		return spec.DAGPlan{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			kind,
			"planning review returned an invalid reviewed plan",
			err,
		)
	}
	return reviewed, nil
}

func validateReviewedPlan(candidate plannerReviewPlanPatch, original spec.DAGPlan, tools []ai.ToolCatalogEntry) (spec.DAGPlan, error) {
	if len(candidate.Edges) == 0 {
		return spec.DAGPlan{}, fmt.Errorf("reviewed plan must contain at least one edge")
	}

	catalog := make(map[string]ai.ToolCatalogEntry, len(tools))
	for _, tool := range tools {
		catalog[tool.Name] = tool
	}

	ids := make(map[string]bool, len(candidate.Edges))
	hasFinalOutput := false
	for _, edge := range candidate.Edges {
		if strings.TrimSpace(edge.ID) == "" {
			return spec.DAGPlan{}, fmt.Errorf("reviewed plan edge id is required")
		}
		if ids[edge.ID] {
			return spec.DAGPlan{}, fmt.Errorf("reviewed plan edge id %q is duplicated", edge.ID)
		}
		ids[edge.ID] = true

		toolSpec, ok := catalog[strings.TrimSpace(edge.ToolName)]
		if !ok {
			return spec.DAGPlan{}, fmt.Errorf("reviewed plan references unknown tool %q", edge.ToolName)
		}
		if !containsType(toolSpec.InputTypes, edge.InputType) {
			return spec.DAGPlan{}, fmt.Errorf("reviewed plan uses unsupported input type %q for tool %q", edge.InputType, edge.ToolName)
		}
		if !containsType(toolSpec.OutputTypes, edge.OutputType) {
			return spec.DAGPlan{}, fmt.Errorf("reviewed plan uses unsupported output type %q for tool %q", edge.OutputType, edge.ToolName)
		}
		if strings.TrimSpace(edge.Title) == "" || strings.TrimSpace(edge.Summary) == "" || strings.TrimSpace(edge.SubTask) == "" {
			return spec.DAGPlan{}, fmt.Errorf("reviewed plan edge %q is missing title, summary, or sub_task", edge.ID)
		}
		if len(cleanExpectedOutputs(edge.ExpectedOutputs)) == 0 {
			return spec.DAGPlan{}, fmt.Errorf("reviewed plan edge %q must declare expected_outputs", edge.ID)
		}
		if strings.EqualFold(strings.TrimSpace(edge.OutputType), "final") {
			hasFinalOutput = true
		}
	}

	for _, edge := range candidate.Edges {
		for _, dependency := range edge.Dependencies {
			dependency = strings.TrimSpace(dependency)
			if dependency == "" {
				continue
			}
			if !ids[dependency] {
				return spec.DAGPlan{}, fmt.Errorf("reviewed plan edge %q depends on unknown edge id %q", edge.ID, dependency)
			}
		}
	}

	if !hasFinalOutput {
		return spec.DAGPlan{}, fmt.Errorf("reviewed plan must produce at least one final output edge")
	}

	plan := original
	plan.Mode = normalizePlanMode(candidate.Mode, candidate.Edges)
	plan.Edges = append([]spec.PlannedEdge(nil), candidate.Edges...)
	plan.ReviewedAt = time.Now().UTC()
	if plan.CreatedAt.IsZero() {
		plan.CreatedAt = time.Now().UTC()
	}
	if plan.Planner == "" {
		plan.Planner = "llm-review-planner"
	}
	return plan, nil
}
