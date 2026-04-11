package runner

import (
	"fmt"
	"strings"

	"github.com/tsumina/dango/internal/ai"
	"github.com/tsumina/dango/internal/spec"
)

type plannerDraftResponse struct {
	Mode  string                     `json:"mode"`
	Edges []plannerDraftResponseEdge `json:"edges"`
}

type plannerDraftResponseEdge struct {
	Ref             string   `json:"ref"`
	ToolName        string   `json:"tool_name"`
	Dependencies    []string `json:"dependencies"`
	InputType       string   `json:"input_type"`
	OutputType      string   `json:"output_type"`
	Title           string   `json:"title"`
	Summary         string   `json:"summary"`
	ExpectedOutputs []string `json:"expected_outputs"`
	SubTask         string   `json:"sub_task"`
}

func normalizePlannerDraft(draft plannerDraftResponse, tools []ai.ToolCatalogEntry) ([]spec.PlannedEdge, string, error) {
	if len(draft.Edges) == 0 {
		return nil, "", fmt.Errorf("planner LLM returned an empty DAG")
	}

	catalog := make(map[string]ai.ToolCatalogEntry, len(tools))
	for _, tool := range tools {
		catalog[tool.Name] = tool
	}

	refToID := map[string]string{}
	plannedEdges := make([]spec.PlannedEdge, 0, len(draft.Edges))
	hasFinalOutput := false

	for index, edge := range draft.Edges {
		ref := strings.TrimSpace(edge.Ref)
		if ref == "" {
			return nil, "", fmt.Errorf("planner edge %d is missing ref", index+1)
		}
		if _, exists := refToID[ref]; exists {
			return nil, "", fmt.Errorf("planner edge ref %q is duplicated", ref)
		}

		toolName := strings.TrimSpace(edge.ToolName)
		toolSpec, ok := catalog[toolName]
		if !ok {
			return nil, "", fmt.Errorf("planner edge %q references unknown tool %q", ref, toolName)
		}

		inputType := strings.TrimSpace(edge.InputType)
		if inputType == "" || !containsType(toolSpec.InputTypes, inputType) {
			return nil, "", fmt.Errorf("planner edge %q uses unsupported input type %q for tool %q", ref, inputType, toolName)
		}
		outputType := strings.TrimSpace(edge.OutputType)
		if outputType == "" || !containsType(toolSpec.OutputTypes, outputType) {
			return nil, "", fmt.Errorf("planner edge %q uses unsupported output type %q for tool %q", ref, outputType, toolName)
		}

		dependencies := make([]string, 0, len(edge.Dependencies))
		for _, dependencyRef := range edge.Dependencies {
			dependencyRef = strings.TrimSpace(dependencyRef)
			if dependencyRef == "" {
				continue
			}
			dependencyID, ok := refToID[dependencyRef]
			if !ok {
				return nil, "", fmt.Errorf("planner edge %q depends on unknown or later ref %q", ref, dependencyRef)
			}
			dependencies = append(dependencies, dependencyID)
		}

		title := strings.TrimSpace(edge.Title)
		if title == "" {
			return nil, "", fmt.Errorf("planner edge %q is missing title", ref)
		}
		summary := strings.TrimSpace(edge.Summary)
		if summary == "" {
			return nil, "", fmt.Errorf("planner edge %q is missing summary", ref)
		}
		subTask := strings.TrimSpace(edge.SubTask)
		if subTask == "" {
			return nil, "", fmt.Errorf("planner edge %q is missing sub_task", ref)
		}

		expectedOutputs := cleanExpectedOutputs(edge.ExpectedOutputs)
		if len(expectedOutputs) == 0 {
			return nil, "", fmt.Errorf("planner edge %q must declare expected_outputs", ref)
		}

		edgeID, err := spec.NewUUID()
		if err != nil {
			return nil, "", err
		}
		refToID[ref] = edgeID

		plannedEdges = append(plannedEdges, spec.PlannedEdge{
			ID:              edgeID,
			ToolName:        toolName,
			Dependencies:    dependencies,
			InputType:       inputType,
			OutputType:      outputType,
			Title:           title,
			Summary:         summary,
			ExpectedOutputs: expectedOutputs,
			SubTask:         subTask,
		})

		if strings.EqualFold(outputType, "final") {
			hasFinalOutput = true
		}
	}

	if !hasFinalOutput {
		return nil, "", fmt.Errorf("planner LLM did not produce a terminal final output edge")
	}

	return plannedEdges, normalizePlanMode(draft.Mode, plannedEdges), nil
}

func cleanExpectedOutputs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func normalizePlanMode(mode string, edges []spec.PlannedEdge) string {
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "dag" || mode == "linear" {
		return mode
	}
	for _, edge := range edges {
		if len(edge.Dependencies) > 1 {
			return "dag"
		}
	}
	return "linear"
}
