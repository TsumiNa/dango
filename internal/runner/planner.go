package runner

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
	"github.com/tsumina/dango/internal/runner/runtime"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
	"github.com/tsumina/dango/internal/taskflow"
)

type catalogTool struct {
	Row  sqlite.ToolRecord
	Spec spec.ToolSpec
}

// Planner coordinates runner-owned draft, detail-refinement, and review planning.
type Planner struct {
	locator    *datadir.Locator
	store      *sqlite.Store
	runtime    runtime.ContainerRuntime
	draftHook  llm.DraftPlanningHook
	reviewHook llm.ReviewPlanningHook
	repairHook llm.RepairPlanningHook
	logger     *slog.Logger
}

// NewPlanner constructs the runner-owned planner with the built-in draft hook.
func NewPlanner(locator *datadir.Locator, store *sqlite.Store, rt runtime.ContainerRuntime, model string, logger *slog.Logger) *Planner {
	return NewPlannerWithClient(locator, store, rt, llm.NewOpenAICompatibleFromEnv(model, logger), logger)
}

// NewPlannerWithClient constructs the runner-owned planner with an explicit LLM client.
func NewPlannerWithClient(locator *datadir.Locator, store *sqlite.Store, rt runtime.ContainerRuntime, client llm.Client, logger *slog.Logger) *Planner {
	return NewPlannerWithHooks(locator, store, rt, newBuiltInDraftPlanningHook(client, logger), newBuiltInReviewPlanningHook(client, logger), newBuiltInRepairPlanningHook(client, logger), logger)
}

// NewPlannerWithHooks constructs the runner-owned planner with explicit hook implementations.
func NewPlannerWithHooks(locator *datadir.Locator, store *sqlite.Store, rt runtime.ContainerRuntime, draftHook llm.DraftPlanningHook, reviewHook llm.ReviewPlanningHook, repairHook llm.RepairPlanningHook, logger *slog.Logger) *Planner {
	return &Planner{
		locator:    locator,
		store:      store,
		runtime:    rt,
		draftHook:  draftHook,
		reviewHook: reviewHook,
		repairHook: repairHook,
		logger:     logging.Component(logger, "runner.planner"),
	}
}

// Plan derives a draft workflow, asks executors to refine their stages, and returns the reviewed DAG.
func (p *Planner) Plan(ctx context.Context, taskID string, request taskflow.RequestEnvelope) (spec.DAGPlan, error) {
	p.logger.Info("planning task", "task_id", taskID)

	tools, err := p.loadCatalog(ctx)
	if err != nil {
		p.logger.Error("failed to load tool catalog", "task_id", taskID, "error", err)
		return spec.DAGPlan{}, err
	}
	if len(tools) == 0 {
		return spec.DAGPlan{}, fmt.Errorf("no tools registered")
	}

	draft, err := p.Draft(ctx, taskID, request, tools)
	if err != nil {
		return spec.DAGPlan{}, err
	}

	refined, err := p.Refine(ctx, taskID, draft)
	if err != nil {
		return spec.DAGPlan{}, err
	}

	plan, err := p.Review(ctx, taskID, request, tools, refined)
	if err != nil {
		return spec.DAGPlan{}, err
	}

	p.logger.Info("planning completed", "task_id", taskID, "edges", len(plan.Edges))
	return plan, nil
}

// Draft creates the initial workflow using the registered tool catalog.
func (p *Planner) Draft(ctx context.Context, taskID string, request taskflow.RequestEnvelope, tools []catalogTool) (spec.DAGPlan, error) {
	if p.draftHook == nil {
		return spec.DAGPlan{}, llm.NewCannotProceedError(
			llm.ModuleRunner,
			llm.KindDraftPlanning,
			"no draft planning hook is available for the runner",
			nil,
		)
	}

	plan, err := p.draftHook.Draft(ctx, llm.DraftPlanRequest{
		TaskID:  taskID,
		Request: request,
		Tools:   catalogEntries(tools),
	})
	if err != nil {
		return spec.DAGPlan{}, err
	}
	return plan, nil
}

// Refine asks executors to refine their own planned stages.
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

// Review validates or adjusts the plan after refinement.
func (p *Planner) Review(ctx context.Context, taskID string, request taskflow.RequestEnvelope, tools []catalogTool, plan spec.DAGPlan) (spec.DAGPlan, error) {
	if p.reviewHook == nil {
		plan.ReviewedAt = time.Now().UTC()
		if plan.Mode == "" {
			plan.Mode = "linear"
		}
		return plan, nil
	}

	reviewed, err := p.reviewHook.Review(ctx, llm.ReviewPlanRequest{
		TaskID:  taskID,
		Request: request,
		Tools:   catalogEntries(tools),
		Plan:    plan,
	})
	if err != nil {
		if p.repairHook == nil {
			return spec.DAGPlan{}, err
		}
		repaired, repairErr := p.repairHook.Repair(ctx, llm.RepairPlanRequest{
			TaskID:  taskID,
			Request: request,
			Tools:   catalogEntries(tools),
			Plan:    plan,
			Reason:  err.Error(),
		})
		if repairErr != nil {
			return spec.DAGPlan{}, repairErr
		}
		reviewed = repaired
	}
	if reviewed.ReviewedAt.IsZero() {
		reviewed.ReviewedAt = time.Now().UTC()
	}
	if reviewed.Mode == "" {
		reviewed.Mode = normalizePlanMode(reviewed.Mode, reviewed.Edges)
	}
	return reviewed, nil
}

func (p *Planner) refineEdge(ctx context.Context, taskID string, edge spec.PlannedEdge, tool sqlite.ToolRecord) (spec.PlannedEdge, error) {
	if p.runtime == nil || p.locator == nil {
		return spec.PlannedEdge{}, llm.NewCannotProceedError(
			llm.ModuleRunner,
			llm.KindDetailPlanning,
			fmt.Sprintf("executor detail planning is unavailable for tool %q", edge.ToolName),
			nil,
		)
	}

	payload, err := p.runtime.PlanExecutor(ctx, runtime.ExecutorPlanRequest{
		Image:          tool.Image,
		TaskID:         taskID,
		SubTaskHost:    p.locator.EdgeSubTaskPath(taskID, edge.ID),
		ToolConfigHost: p.locator.ToolMergedPath(edge.ToolName),
	})
	if err != nil {
		return spec.PlannedEdge{}, llm.NewCannotProceedError(
			llm.ModuleRunner,
			llm.KindDetailPlanning,
			fmt.Sprintf("executor detail planning failed for tool %q", edge.ToolName),
			err,
		)
	}

	var plan spec.ExecutorPlan
	if err := json.Unmarshal(payload, &plan); err != nil {
		return spec.PlannedEdge{}, llm.NewCannotProceedError(
			llm.ModuleRunner,
			llm.KindDetailPlanning,
			fmt.Sprintf("executor detail planning returned invalid output for tool %q", edge.ToolName),
			err,
		)
	}
	if !executorPlanProvidesDetail(plan) {
		return spec.PlannedEdge{}, llm.NewCannotProceedError(
			llm.ModuleRunner,
			llm.KindDetailPlanning,
			fmt.Sprintf("executor detail planning returned no usable detail for tool %q", edge.ToolName),
			nil,
		)
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

	if strings.TrimSpace(edge.Summary) == "" || strings.TrimSpace(edge.SubTask) == "" {
		return spec.PlannedEdge{}, llm.NewCannotProceedError(
			llm.ModuleRunner,
			llm.KindDetailPlanning,
			fmt.Sprintf("executor detail planning left required fields empty for tool %q", edge.ToolName),
			nil,
		)
	}
	if len(edge.ExpectedOutputs) == 0 {
		return spec.PlannedEdge{}, llm.NewCannotProceedError(
			llm.ModuleRunner,
			llm.KindDetailPlanning,
			fmt.Sprintf("executor detail planning did not declare expected outputs for tool %q", edge.ToolName),
			nil,
		)
	}

	return edge, nil
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

func catalogEntries(tools []catalogTool) []llm.ToolCatalogEntry {
	entries := make([]llm.ToolCatalogEntry, 0, len(tools))
	for _, tool := range tools {
		entries = append(entries, llm.ToolCatalogEntry{
			Name:        tool.Spec.Name,
			Description: tool.Spec.Description,
			InputTypes:  append([]string(nil), tool.Spec.InputTypes...),
			OutputTypes: append([]string(nil), tool.Spec.OutputTypes...),
			Model:       tool.Spec.Model,
		})
	}
	return entries
}

func executorPlanProvidesDetail(plan spec.ExecutorPlan) bool {
	return strings.TrimSpace(plan.Summary) != "" || strings.TrimSpace(plan.SubTask) != "" || len(plan.ExpectedOutputs) > 0
}

func containsType(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
