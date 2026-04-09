package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
)

// DemoEngine runs the end-to-end local demo orchestration flow.
type DemoEngine struct {
	locator   *datadir.Locator
	store     *sqlite.Store
	tasks     *TaskService
	planner   *Planner
	scheduler *Scheduler
	logger    *slog.Logger
}

// DemoRunResult summarizes a completed demo execution.
type DemoRunResult struct {
	// Task is the final persisted task row.
	Task sqlite.TaskRecord `json:"task"`
	// Plan is the plan executed for the task.
	Plan spec.DAGPlan `json:"plan"`
	// TerminalHandoffs contains frontmatter summaries from terminal edges.
	TerminalHandoffs []spec.HandoffMetadata `json:"terminal_handoffs"`
	// TaskDir is the task directory on disk.
	TaskDir string `json:"task_dir"`
	// ResultPath is the result.md path written by the engine.
	ResultPath string `json:"result_path"`
}

// NewDemoEngine constructs the demo orchestration engine used by the local
// runnable sample.
func NewDemoEngine(locator *datadir.Locator, store *sqlite.Store, tasks *TaskService, planner *Planner, scheduler *Scheduler, logger *slog.Logger) *DemoEngine {
	return &DemoEngine{
		locator:   locator,
		store:     store,
		tasks:     tasks,
		planner:   planner,
		scheduler: scheduler,
		logger:    logging.Component(logger, "orchestrator.engine"),
	}
}

// Run executes the end-to-end demo flow for one request.
func (e *DemoEngine) Run(ctx context.Context, request string) (*DemoRunResult, error) {
	task, taskLogger, err := e.createTask(ctx, request)
	if err != nil {
		return nil, err
	}

	task, plan, err := e.planTask(ctx, taskLogger, task, request)
	if err != nil {
		return nil, err
	}

	handoffs, err := e.executePlan(ctx, taskLogger, task, request, plan)
	if err != nil {
		return nil, err
	}

	return e.completeTask(ctx, taskLogger, task, request, plan, handoffs)
}

func (e *DemoEngine) createTask(ctx context.Context, request string) (sqlite.TaskRecord, *slog.Logger, error) {
	e.logger.Info("demo run started")
	task, err := e.tasks.Create(ctx, request)
	if err != nil {
		e.logger.Error("failed to create task", "error", err)
		return sqlite.TaskRecord{}, nil, err
	}

	taskLogger := e.logger.With("task_id", task.ID)
	return task, taskLogger, nil
}

func (e *DemoEngine) planTask(ctx context.Context, taskLogger *slog.Logger, task sqlite.TaskRecord, request string) (sqlite.TaskRecord, spec.DAGPlan, error) {
	plan, err := e.planner.Plan(ctx, task.ID, request)
	if err != nil {
		taskLogger.Error("planning failed", "error", err)
		e.markTaskFailed(ctx, task.ID, buildFailureResult(task.ID, request, err))
		return sqlite.TaskRecord{}, spec.DAGPlan{}, err
	}

	task, err = e.tasks.ApplyPlan(ctx, task.ID, plan, spec.TaskStatusApproved)
	if err != nil {
		taskLogger.Error("failed to persist approved plan", "error", err)
		return sqlite.TaskRecord{}, spec.DAGPlan{}, err
	}

	task, err = e.tasks.UpdateStatus(ctx, task.ID, spec.TaskStatusExecuting)
	if err != nil {
		taskLogger.Error("failed to transition task to executing", "error", err)
		return sqlite.TaskRecord{}, spec.DAGPlan{}, err
	}

	return task, plan, nil
}

func (e *DemoEngine) executePlan(ctx context.Context, taskLogger *slog.Logger, task sqlite.TaskRecord, request string, plan spec.DAGPlan) ([]spec.Handoff, error) {
	handoffs := make([]spec.Handoff, 0, len(plan.Edges))
	for _, edge := range plan.Edges {
		taskLogger.Info("dispatching edge", "edge_id", edge.ID, "tool", edge.ToolName, "upstream", edge.Upstream)
		handoff, edgeErr := e.scheduler.RunLocalEdge(ctx, EdgeExecutionRequest{
			TaskID:         task.ID,
			EdgeID:         edge.ID,
			ToolName:       edge.ToolName,
			UpstreamEdgeID: edge.Upstream,
			SubTaskContent: edge.SubTask,
		})
		if edgeErr != nil {
			taskLogger.Error("edge execution failed", "edge_id", edge.ID, "tool", edge.ToolName, "error", edgeErr)
			e.markTaskFailed(ctx, task.ID, buildExecutionFailureResult(task.ID, request, plan, handoffs, edgeErr))
			return nil, edgeErr
		}

		taskLogger.Info("edge completed", "edge_id", edge.ID, "tool", edge.ToolName, "status", handoff.Metadata.Status)
		handoffs = append(handoffs, handoff)
	}

	return handoffs, nil
}

func (e *DemoEngine) completeTask(ctx context.Context, taskLogger *slog.Logger, task sqlite.TaskRecord, request string, plan spec.DAGPlan, handoffs []spec.Handoff) (*DemoRunResult, error) {
	task, err := e.finalizeTaskStatus(ctx, task.ID, spec.TaskStatusDone)
	if err != nil {
		taskLogger.Error("failed to transition task to done", "error", err)
		return nil, err
	}

	terminalHandoffs := extractTerminalHandoffs(plan, handoffs)
	if err := e.tasks.WriteResult(task.ID, buildSuccessResult(task.ID, request, plan, terminalHandoffs, e.locator)); err != nil {
		taskLogger.Error("failed to write final result", "error", err)
		return nil, err
	}

	taskLogger.Info("demo run completed", "terminal_handoffs", len(terminalHandoffs), "result_path", e.locator.TaskResultPath(task.ID))

	return &DemoRunResult{
		Task:             task,
		Plan:             plan,
		TerminalHandoffs: metadataOnly(terminalHandoffs),
		TaskDir:          e.locator.TaskDir(task.ID),
		ResultPath:       e.locator.TaskResultPath(task.ID),
	}, nil
}

func (e *DemoEngine) finalizeTaskStatus(ctx context.Context, taskID string, status spec.TaskStatus) (sqlite.TaskRecord, error) {
	finalizeCtx, cancel := finalizeContext(ctx)
	defer cancel()

	return e.tasks.UpdateStatus(finalizeCtx, taskID, status)
}

func (e *DemoEngine) markTaskFailed(ctx context.Context, taskID string, result string) {
	finalizeCtx, cancel := finalizeContext(ctx)
	defer cancel()

	_, _ = e.tasks.UpdateStatus(finalizeCtx, taskID, spec.TaskStatusFailed)
	_ = e.tasks.WriteResult(taskID, result)
}

func extractTerminalHandoffs(plan spec.DAGPlan, handoffs []spec.Handoff) []spec.Handoff {
	if len(plan.Edges) == 0 || len(handoffs) == 0 {
		return nil
	}

	upstreamSet := map[string]bool{}
	for _, edge := range plan.Edges {
		if edge.Upstream != "" {
			upstreamSet[edge.Upstream] = true
		}
	}

	handoffByEdge := make(map[string]spec.Handoff, len(handoffs))
	for index, handoff := range handoffs {
		if index < len(plan.Edges) {
			handoffByEdge[plan.Edges[index].ID] = handoff
		}
	}

	var out []spec.Handoff
	for _, edge := range plan.Edges {
		if upstreamSet[edge.ID] {
			continue
		}
		if handoff, ok := handoffByEdge[edge.ID]; ok {
			out = append(out, handoff)
		}
	}

	return out
}

func metadataOnly(handoffs []spec.Handoff) []spec.HandoffMetadata {
	out := make([]spec.HandoffMetadata, 0, len(handoffs))
	for _, handoff := range handoffs {
		out = append(out, handoff.Metadata)
	}
	return out
}

func buildSuccessResult(taskID, request string, plan spec.DAGPlan, terminal []spec.Handoff, locator *datadir.Locator) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "# Demo Result\n\nTask ID: `%s`\n\n", taskID)
	_, _ = fmt.Fprintf(&b, "Request:\n\n%s\n\n", strings.TrimSpace(request))
	_, _ = fmt.Fprintf(&b, "Plan: `%d` stage(s)\n\n", len(plan.Edges))
	_, _ = fmt.Fprintln(&b, "## Terminal Outputs")
	for _, handoff := range terminal {
		edgeID := findEdgeIDForTool(plan, handoff.Metadata.Tool)
		_, _ = fmt.Fprintf(&b, "- Tool `%s` finished with status `%s`\n", handoff.Metadata.Tool, handoff.Metadata.Status)
		_, _ = fmt.Fprintf(&b, "  Output dir: `%s`\n", locator.EdgeOutputDir(taskID, edgeID))
		if len(handoff.Metadata.OutputFiles) == 0 {
			_, _ = fmt.Fprintln(&b, "  Files: none declared")
			continue
		}
		_, _ = fmt.Fprintf(&b, "  Files: %s\n", strings.Join(handoff.Metadata.OutputFiles, ", "))
	}
	_, _ = fmt.Fprintf(&b, "\nResult path: `%s`\n", locator.TaskResultPath(taskID))
	return b.String()
}

func buildFailureResult(taskID, request string, err error) string {
	return fmt.Sprintf("# Demo Result\n\nTask ID: `%s`\n\nRequest:\n\n%s\n\nStatus: failed during planning\n\nError: %s\n", taskID, strings.TrimSpace(request), err)
}

func buildExecutionFailureResult(taskID, request string, plan spec.DAGPlan, handoffs []spec.Handoff, err error) string {
	return fmt.Sprintf("# Demo Result\n\nTask ID: `%s`\n\nRequest:\n\n%s\n\nPlanned stages: `%d`\nCompleted handoffs: `%d`\n\nStatus: failed during execution\n\nError: %s\n", taskID, strings.TrimSpace(request), len(plan.Edges), len(handoffs), err)
}

func findEdgeIDForTool(plan spec.DAGPlan, toolName string) string {
	for _, edge := range plan.Edges {
		if edge.ToolName == toolName {
			return edge.ID
		}
	}
	return ""
}
