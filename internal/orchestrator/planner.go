package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
)

// Planner derives a demo execution plan from the registered tool catalog.
type Planner struct {
	store  *sqlite.Store
	logger *slog.Logger
}

type catalogTool struct {
	Row  sqlite.ToolRecord
	Spec spec.ToolSpec
}

type pathStep struct {
	Tool       catalogTool
	InputType  string
	OutputType string
}

type plannerState struct {
	CurrentType string
	UsedTools   map[string]bool
	Steps       []pathStep
}

// NewPlanner constructs the demo planner used to derive a linear tool path
// from the registered tool catalog.
func NewPlanner(store *sqlite.Store, logger *slog.Logger) *Planner {
	return &Planner{
		store:  store,
		logger: logging.Component(logger, "orchestrator.planner"),
	}
}

// Plan builds a demo DAG for request by finding a linear path from the
// synthetic request input type to the synthetic final output type.
func (p *Planner) Plan(ctx context.Context, taskID, request string) (spec.DAGPlan, error) {
	p.logger.Info("planning task", "task_id", taskID)
	tools, err := p.loadCatalog(ctx)
	if err != nil {
		p.logger.Error("failed to load tool catalog", "task_id", taskID, "error", err)
		return spec.DAGPlan{}, err
	}
	if len(tools) == 0 {
		p.logger.Warn("no tools registered for planning", "task_id", taskID)
		return spec.DAGPlan{}, fmt.Errorf("no tools registered")
	}

	steps, err := findLinearPath(tools, "request", "final")
	if err != nil {
		p.logger.Error("planner failed to find path", "task_id", taskID, "error", err)
		return spec.DAGPlan{}, err
	}

	edges := make([]spec.PlannedEdge, 0, len(steps))
	upstream := ""
	for index, step := range steps {
		edgeID, err := spec.NewUUID()
		if err != nil {
			return spec.DAGPlan{}, err
		}

		edge := spec.PlannedEdge{
			ID:         edgeID,
			ToolName:   step.Tool.Spec.Name,
			Upstream:   upstream,
			InputType:  step.InputType,
			OutputType: step.OutputType,
			SubTask:    buildSubTaskMarkdown(taskID, request, step.Tool.Spec, step.InputType, step.OutputType, index+1, len(steps)),
		}
		edges = append(edges, edge)
		upstream = edgeID
	}

	selectedTools := make([]string, 0, len(edges))
	for _, edge := range edges {
		selectedTools = append(selectedTools, edge.ToolName)
	}
	p.logger.Info("planning completed", "task_id", taskID, "edges", len(edges), "tools", selectedTools)

	return spec.DAGPlan{
		Planner:   "demo-rule-planner",
		Mode:      "linear",
		CreatedAt: time.Now().UTC(),
		Edges:     edges,
	}, nil
}

func (p *Planner) loadCatalog(ctx context.Context) ([]catalogTool, error) {
	rows, err := p.store.ListTools(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]catalogTool, 0, len(rows))
	for _, row := range rows {
		var toolSpec spec.ToolSpec
		if err := json.Unmarshal([]byte(row.ConfigJSON), &toolSpec); err != nil {
			return nil, fmt.Errorf("decode config json for tool %q: %w", row.Name, err)
		}
		if err := toolSpec.Validate(); err != nil {
			return nil, fmt.Errorf("validate config for tool %q: %w", row.Name, err)
		}
		out = append(out, catalogTool{
			Row:  row,
			Spec: toolSpec,
		})
	}

	return out, nil
}

func findLinearPath(tools []catalogTool, startType, goalType string) ([]pathStep, error) {
	queue := []plannerState{{
		CurrentType: startType,
		UsedTools:   map[string]bool{},
	}}

	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]

		for _, tool := range tools {
			if state.UsedTools[tool.Spec.Name] {
				continue
			}
			if !containsType(tool.Spec.InputTypes, state.CurrentType) {
				continue
			}

			for _, outputType := range tool.Spec.OutputTypes {
				nextSteps := appendPathStep(state.Steps, pathStep{
					Tool:       tool,
					InputType:  state.CurrentType,
					OutputType: outputType,
				})

				if outputType == goalType {
					return nextSteps, nil
				}

				nextUsed := cloneToolSet(state.UsedTools)
				nextUsed[tool.Spec.Name] = true
				queue = append(queue, plannerState{
					CurrentType: outputType,
					UsedTools:   nextUsed,
					Steps:       nextSteps,
				})
			}
		}
	}

	return nil, fmt.Errorf("no tool path found from %q to %q", startType, goalType)
}

func buildSubTaskMarkdown(taskID, request string, tool spec.ToolSpec, inputType, outputType string, index, total int) string {
	request = strings.TrimSpace(request)
	if request == "" {
		request = "(empty request)"
	}

	return fmt.Sprintf("# Sub-task\n\n"+
		"Task ID: %s\n"+
		"Stage: %d/%d\n"+
		"Assigned tool: %s\n\n"+
		"## Objective\n\n"+
		"Use this tool to transform `%s` into `%s`.\n\n"+
		"## Tool Context\n\n"+
		"- Name: %s\n"+
		"- Description: %s\n"+
		"- Model: %s\n\n"+
		"## Execution Notes\n\n"+
		"- If this is the first stage, rely on the original request below and the local config.\n"+
		"- If this is not the first stage, read from INPUT_PATH and produce output files in OUTPUT_PATH.\n"+
		"- Always write a valid _handoff.md at the root of OUTPUT_PATH.\n"+
		"- Keep artifacts small and review-friendly for the demo.\n\n"+
		"## Original Request\n\n"+
		"%s\n",
		taskID, index, total, tool.Name, inputType, outputType, tool.Name, tool.Description, tool.Model, request)
}

func containsType(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func cloneToolSet(value map[string]bool) map[string]bool {
	out := make(map[string]bool, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func appendPathStep(base []pathStep, step pathStep) []pathStep {
	out := make([]pathStep, 0, len(base)+1)
	out = append(out, base...)
	out = append(out, step)
	return out
}
