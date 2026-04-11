package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/ai"
	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/logging"
	promptassets "github.com/tsumina/dango/internal/prompts"
	"github.com/tsumina/dango/internal/runner/runtime"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
	"github.com/tsumina/dango/internal/taskflow"
)

type catalogTool struct {
	Row  sqlite.ToolRecord
	Spec spec.ToolSpec
}

// Planner coordinates the runner-owned planning pipeline for one task.
//
// Planner turns a normalized request plus the current tool catalog into a
// reviewed executable DAG. It owns four planning stages: loading the catalog,
// creating the initial draft, refining each edge through executor-side detail
// planning, and reviewing or repairing the resulting DAG before execution. The
// zero value is not usable; callers construct it with [NewPlanner].
type Planner struct {
	locator   *datadir.Locator
	store     *sqlite.Store
	runtime   runtime.ContainerRuntime
	llmClient ai.Client
	logger    *slog.Logger
}

// NewPlanner constructs a [Planner] with an explicit AI client.
//
// This is the standard constructor used for production wiring and tests. Pass
// nil to disable AI-backed planning; the planner will still stamp the plan as
// reviewed and return it unchanged.
func NewPlanner(locator *datadir.Locator, store *sqlite.Store, rt runtime.ContainerRuntime, client ai.Client, logger *slog.Logger) *Planner {
	return &Planner{
		locator:   locator,
		store:     store,
		runtime:   rt,
		llmClient: client,
		logger:    logging.Component(logger, "runner.planner"),
	}
}

// Plan runs the full planning pipeline and returns the reviewed executable DAG.
//
// The workflow is: load the registered tool catalog, create the initial draft,
// refine each edge through executor planning, and then review or repair the
// resulting DAG. Plan does not persist the DAG itself; the caller is expected
// to store the returned plan before execution so later describe, resume, and
// result-assembly paths observe the same reviewed graph.
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

// Draft creates the initial workflow from the normalized request and tool
// catalog.
//
// Draft is the first dynamic planning stage. It calls the LLM with the
// draft-planning prompt and returns a syntactic DAG before executor-side
// detail refinement.
func (p *Planner) Draft(ctx context.Context, taskID string, request taskflow.RequestEnvelope, tools []catalogTool) (spec.DAGPlan, error) {
	if p.llmClient == nil {
		return spec.DAGPlan{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindDraftPlanning,
			"no LLM client is configured for the runner planner",
			nil,
		)
	}

	prompt, err := promptassets.RenderPlannerDraft(taskID, taskflow.PrimaryRequestText(request), catalogEntries(tools))
	if err != nil {
		return spec.DAGPlan{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindDraftPlanning,
			"failed to render draft planning prompt",
			err,
		)
	}

	payload, _, err := p.llmClient.CompleteJSON(ctx, ai.Request{
		SystemPrompt: prompt,
		UserPrompt:   "Generate the task DAG now and return JSON only.",
		Temperature:  0.1,
	})
	if err != nil {
		return spec.DAGPlan{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindDraftPlanning,
			"draft planning LLM call failed",
			err,
		)
	}

	var draft plannerDraftResponse
	if err := json.Unmarshal(payload, &draft); err != nil {
		return spec.DAGPlan{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindDraftPlanning,
			"draft planning LLM returned invalid JSON",
			err,
		)
	}

	edges, mode, err := normalizePlannerDraft(draft, catalogEntries(tools))
	if err != nil {
		return spec.DAGPlan{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindDraftPlanning,
			"draft planning LLM returned an invalid plan",
			err,
		)
	}

	return spec.DAGPlan{
		Planner:   "llm-draft-planner",
		Mode:      mode,
		Revision:  1,
		CreatedAt: time.Now().UTC(),
		Edges:     edges,
	}, nil
}

// Refine asks each executor to refine its own planned stage.
//
// Refine writes the current edge sub-task markdown into the task directory,
// calls executor detail planning through the runtime abstraction, and merges
// the returned summary, sub-task text, and expected outputs back into the DAG.
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

// Review validates or adjusts the refined plan before execution.
//
// When a LLM client is configured, Review asks it to approve or correct the
// plan and optionally falls back to a repair call when review fails. When no
// LLM client is present, Review stamps the plan as reviewed and returns it.
func (p *Planner) Review(ctx context.Context, taskID string, request taskflow.RequestEnvelope, tools []catalogTool, plan spec.DAGPlan) (spec.DAGPlan, error) {
	if p.llmClient == nil {
		plan.ReviewedAt = time.Now().UTC()
		if plan.Mode == "" {
			plan.Mode = "linear"
		}
		return plan, nil
	}

	prompt, err := promptassets.RenderPlannerReview(taskID, request, catalogEntries(tools), plan)
	if err != nil {
		return spec.DAGPlan{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindReviewPlanning,
			"failed to render review planning prompt",
			err,
		)
	}

	payload, _, err := p.llmClient.CompleteJSON(ctx, ai.Request{
		SystemPrompt: prompt,
		UserPrompt:   "Review the current plan now and return JSON only.",
		Temperature:  0.1,
	})
	if err != nil {
		return spec.DAGPlan{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindReviewPlanning,
			"review planning LLM call failed",
			err,
		)
	}

	reviewed, reviewErr := normalizeReviewedPlanResponse(payload, plan, catalogEntries(tools), ai.KindReviewPlanning)
	if reviewErr == nil {
		if reviewed.ReviewedAt.IsZero() {
			reviewed.ReviewedAt = time.Now().UTC()
		}
		if reviewed.Mode == "" {
			reviewed.Mode = normalizePlanMode(reviewed.Mode, reviewed.Edges)
		}
		return reviewed, nil
	}

	// Attempt repair when review fails.
	repairPrompt, err := promptassets.RenderPlannerRepair(taskID, request, catalogEntries(tools), plan, reviewErr.Error())
	if err != nil {
		return spec.DAGPlan{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindRepairPlanning,
			"failed to render repair planning prompt",
			err,
		)
	}

	repairPayload, _, err := p.llmClient.CompleteJSON(ctx, ai.Request{
		SystemPrompt: repairPrompt,
		UserPrompt:   "Repair the current plan now and return JSON only.",
		Temperature:  0.1,
	})
	if err != nil {
		return spec.DAGPlan{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindRepairPlanning,
			"repair planning LLM call failed",
			err,
		)
	}

	repaired, err := normalizeReviewedPlanResponse(repairPayload, plan, catalogEntries(tools), ai.KindRepairPlanning)
	if err != nil {
		return spec.DAGPlan{}, err
	}
	if repaired.ReviewedAt.IsZero() {
		repaired.ReviewedAt = time.Now().UTC()
	}
	if repaired.Mode == "" {
		repaired.Mode = normalizePlanMode(repaired.Mode, repaired.Edges)
	}
	return repaired, nil
}

func (p *Planner) refineEdge(ctx context.Context, taskID string, edge spec.PlannedEdge, tool sqlite.ToolRecord) (spec.PlannedEdge, error) {
	if p.runtime == nil || p.locator == nil {
		return spec.PlannedEdge{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindDetailPlanning,
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
		return spec.PlannedEdge{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindDetailPlanning,
			fmt.Sprintf("executor detail planning failed for tool %q", edge.ToolName),
			err,
		)
	}

	var plan spec.ExecutorPlan
	if err := json.Unmarshal(payload, &plan); err != nil {
		return spec.PlannedEdge{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindDetailPlanning,
			fmt.Sprintf("executor detail planning returned invalid output for tool %q", edge.ToolName),
			err,
		)
	}
	if !executorPlanProvidesDetail(plan) {
		return spec.PlannedEdge{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindDetailPlanning,
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
		return spec.PlannedEdge{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindDetailPlanning,
			fmt.Sprintf("executor detail planning left required fields empty for tool %q", edge.ToolName),
			nil,
		)
	}
	if len(edge.ExpectedOutputs) == 0 {
		return spec.PlannedEdge{}, ai.NewCannotProceedError(
			ai.ModuleRunner,
			ai.KindDetailPlanning,
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

func catalogEntries(tools []catalogTool) []ai.ToolCatalogEntry {
	entries := make([]ai.ToolCatalogEntry, 0, len(tools))
	for _, tool := range tools {
		entries = append(entries, ai.ToolCatalogEntry{
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
