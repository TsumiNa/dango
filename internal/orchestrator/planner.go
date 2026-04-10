package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/runtime"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
)

// Planner derives a task workflow from the registered tool catalog and asks
// executors to refine their own assigned stages when possible.
type Planner struct {
	locator *datadir.Locator
	store   *sqlite.Store
	runtime runtime.ContainerRuntime
	llm     llm.Client
	logger  *slog.Logger
}

type catalogTool struct {
	Row  sqlite.ToolRecord
	Spec spec.ToolSpec
}

// NewPlanner constructs the planner used to derive and refine a task DAG.
func NewPlanner(locator *datadir.Locator, store *sqlite.Store, rt runtime.ContainerRuntime, model string, logger *slog.Logger) *Planner {
	return NewPlannerWithClient(locator, store, rt, llm.NewOpenAICompatibleFromEnv(model, logger), logger)
}

// NewPlannerWithClient constructs the planner with an explicit LLM client.
func NewPlannerWithClient(locator *datadir.Locator, store *sqlite.Store, rt runtime.ContainerRuntime, client llm.Client, logger *slog.Logger) *Planner {
	return &Planner{
		locator: locator,
		store:   store,
		runtime: rt,
		llm:     client,
		logger:  logging.Component(logger, "orchestrator.planner"),
	}
}

// Plan derives a draft workflow, asks executors to refine their stages, and
// returns the runner-reviewed DAG.
func (p *Planner) Plan(ctx context.Context, taskID, request string) (spec.DAGPlan, error) {
	p.logger.Info("planning task", "task_id", taskID)

	draft, err := p.Draft(ctx, taskID, request)
	if err != nil {
		return spec.DAGPlan{}, err
	}

	refined, err := p.Refine(ctx, taskID, draft)
	if err != nil {
		return spec.DAGPlan{}, err
	}

	plan := p.review(refined)
	p.logger.Info("planning completed", "task_id", taskID, "edges", len(plan.Edges))
	return plan, nil
}

// Draft creates the initial checklist-shaped workflow using the registered tool catalog.
func (p *Planner) Draft(ctx context.Context, taskID, request string) (spec.DAGPlan, error) {
	tools, err := p.loadCatalog(ctx)
	if err != nil {
		p.logger.Error("failed to load tool catalog", "task_id", taskID, "error", err)
		return spec.DAGPlan{}, err
	}
	if len(tools) == 0 {
		return spec.DAGPlan{}, fmt.Errorf("no tools registered")
	}

	plan, err := p.buildDraftWithLLM(ctx, taskID, request, tools)
	if err != nil {
		p.logger.Error("planner LLM draft failed", "task_id", taskID, "error", err)
		return spec.DAGPlan{}, err
	}
	return plan, nil
}

// Refine asks executors to refine their own planned stages and falls back to a
// generic scaffold when no planner-specific hook is available.
func (p *Planner) Refine(ctx context.Context, taskID string, draft spec.DAGPlan) (spec.DAGPlan, error) {
	refined := draft
	refined.Planner = "runner-refine-planner"
	refined.Edges = append([]spec.PlannedEdge(nil), draft.Edges...)

	for index, edge := range refined.Edges {
		tool, err := p.store.GetTool(ctx, edge.ToolName)
		if err != nil {
			return spec.DAGPlan{}, err
		}
		if p.locator != nil {
			if err := p.locator.EnsureEdgeDir(taskID, edge.ID); err != nil {
				return spec.DAGPlan{}, err
			}
			if err := os.WriteFile(p.locator.EdgeSubTaskPath(taskID, edge.ID), []byte(edge.SubTask), 0o644); err != nil {
				return spec.DAGPlan{}, fmt.Errorf("write planning sub-task: %w", err)
			}
		}

		updatedEdge, err := p.refineEdge(ctx, taskID, edge, tool)
		if err != nil {
			return spec.DAGPlan{}, err
		}
		if updatedEdge.Title == "" {
			updatedEdge.Title = fmt.Sprintf("Stage %d", index+1)
		}
		refined.Edges[index] = updatedEdge
	}

	return refined, nil
}

func (p *Planner) refineEdge(ctx context.Context, taskID string, edge spec.PlannedEdge, tool sqlite.ToolRecord) (spec.PlannedEdge, error) {
	if p.runtime == nil || p.locator == nil {
		return p.fallbackEdgePlan(edge), nil
	}

	payload, err := p.runtime.PlanExecutor(ctx, runtime.ExecutorPlanRequest{
		Image:          tool.Image,
		TaskID:         taskID,
		SubTaskHost:    p.locator.EdgeSubTaskPath(taskID, edge.ID),
		ToolConfigHost: p.locator.ToolMergedPath(edge.ToolName),
	})
	if err != nil {
		p.logger.Warn("executor planning hook unavailable; using fallback refinement", "task_id", taskID, "edge_id", edge.ID, "tool", edge.ToolName, "error", err)
		return p.fallbackEdgePlan(edge), nil
	}

	var plan spec.ExecutorPlan
	if err := json.Unmarshal(payload, &plan); err != nil {
		p.logger.Warn("executor planning output was invalid; using fallback refinement", "task_id", taskID, "edge_id", edge.ID, "tool", edge.ToolName, "error", err)
		return p.fallbackEdgePlan(edge), nil
	}

	if strings.TrimSpace(plan.Summary) != "" {
		edge.Summary = strings.TrimSpace(plan.Summary)
	}
	if strings.TrimSpace(plan.SubTask) != "" {
		edge.SubTask = strings.TrimSpace(plan.SubTask)
	}
	if len(plan.ExpectedOutputs) > 0 {
		edge.ExpectedOutputs = append([]string(nil), plan.ExpectedOutputs...)
	}

	return p.fallbackEdgePlan(edge), nil
}

func (p *Planner) fallbackEdgePlan(edge spec.PlannedEdge) spec.PlannedEdge {
	if strings.TrimSpace(edge.Summary) == "" {
		edge.Summary = fmt.Sprintf("Use %s to transform %s into %s.", edge.ToolName, edge.InputType, edge.OutputType)
	}
	if len(edge.ExpectedOutputs) == 0 {
		edge.ExpectedOutputs = []string{"result." + strings.TrimSpace(edge.OutputType)}
	}
	if strings.TrimSpace(edge.SubTask) == "" {
		edge.SubTask = fmt.Sprintf("Complete the stage assigned to %s.", edge.ToolName)
	}
	return edge
}

func (p *Planner) review(plan spec.DAGPlan) spec.DAGPlan {
	plan.ReviewedAt = time.Now().UTC()
	if plan.Mode == "" {
		plan.Mode = "linear"
	}
	return plan
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
		out = append(out, catalogTool{Row: row, Spec: toolSpec})
	}

	return out, nil
}

func containsType(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func defaultExpectedOutputs(tool spec.ToolSpec, outputType string) []string {
	if strings.TrimSpace(outputType) != "" {
		return []string{"result." + strings.TrimSpace(outputType)}
	}
	if len(tool.OutputTypes) == 0 {
		return nil
	}
	return []string{"result." + strings.TrimSpace(tool.OutputTypes[0])}
}
