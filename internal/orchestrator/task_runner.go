package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
)

// TaskRunResult summarizes a completed task runner execution.
type TaskRunResult struct {
	// Task is the final persisted task row.
	Task sqlite.TaskRecord `json:"task"`
	// Plan is the plan executed for the task.
	Plan spec.DAGPlan `json:"plan"`
	// TerminalHandoffs contains frontmatter summaries from terminal edges.
	TerminalHandoffs []spec.HandoffMetadata `json:"terminal_handoffs"`
	// TaskDir is the task directory on disk.
	TaskDir string `json:"task_dir"`
	// ResultPath is the result.md path written by the runner.
	ResultPath string `json:"result_path"`
}

type terminalEdgeResult struct {
	Edge    spec.PlannedEdge
	Handoff spec.Handoff
}

// TaskRunner manages the full lifecycle of one task execution.
type TaskRunner struct {
	locator   *datadir.Locator
	tasks     *TaskService
	planner   *Planner
	scheduler *Scheduler
	logger    *slog.Logger
	task      sqlite.TaskRecord
	metadata  TaskMetadata
}

// NewTaskRunner constructs a runner for one persisted task.
func NewTaskRunner(locator *datadir.Locator, tasks *TaskService, planner *Planner, scheduler *Scheduler, task sqlite.TaskRecord, metadata TaskMetadata, logger *slog.Logger) *TaskRunner {
	return &TaskRunner{
		locator:   locator,
		tasks:     tasks,
		planner:   planner,
		scheduler: scheduler,
		logger:    logging.Component(logger, "orchestrator.task_runner"),
		task:      task,
		metadata:  metadata,
	}
}

// Run executes planning, review, dispatch, execution, and finalization for one task.
func (r *TaskRunner) Run(ctx context.Context) (*TaskRunResult, error) {
	request := primaryRequestText(r.metadata.Request)
	if strings.TrimSpace(request) == "" {
		request = r.task.Request
	}
	taskLogger := r.logger.With("task_id", r.task.ID)
	_ = r.tasks.AppendEvent(r.task.ID, TaskEvent{Timestamp: nowUTC(), Type: "task.run.started", Message: "task runner execution started"})

	task, plan, err := r.planTask(ctx, taskLogger, request)
	if err != nil {
		return nil, err
	}

	handoffs, err := r.executePlan(ctx, taskLogger, task, request, plan)
	if err != nil {
		return nil, err
	}

	return r.completeTask(ctx, taskLogger, task, request, plan, handoffs)
}

func (r *TaskRunner) planTask(ctx context.Context, taskLogger *slog.Logger, request string) (sqlite.TaskRecord, spec.DAGPlan, error) {
	task, err := r.tasks.UpdateStatus(ctx, r.task.ID, spec.TaskStatusPlanning)
	if err != nil {
		return sqlite.TaskRecord{}, spec.DAGPlan{}, err
	}

	plan, err := r.planner.Plan(ctx, task.ID, request)
	if err != nil {
		taskLogger.Error("planning failed", "error", err)
		r.markTaskStopped(ctx, task.ID, request, spec.TaskStatusFailed, buildFailureResult(task.ID, request, err))
		return sqlite.TaskRecord{}, spec.DAGPlan{}, err
	}

	task, err = r.tasks.ApplyPlan(ctx, task.ID, plan, spec.TaskStatusReviewing)
	if err != nil {
		return sqlite.TaskRecord{}, spec.DAGPlan{}, err
	}
	if task, err = r.tasks.UpdateStatus(ctx, task.ID, spec.TaskStatusApproved); err != nil {
		return sqlite.TaskRecord{}, spec.DAGPlan{}, err
	}
	if task, err = r.tasks.UpdateStatus(ctx, task.ID, spec.TaskStatusExecuting); err != nil {
		return sqlite.TaskRecord{}, spec.DAGPlan{}, err
	}
	return task, plan, nil
}

func (r *TaskRunner) executePlan(ctx context.Context, taskLogger *slog.Logger, task sqlite.TaskRecord, request string, plan spec.DAGPlan) ([]spec.Handoff, error) {
	handoffs := make([]spec.Handoff, 0, len(plan.Edges))
	for _, edge := range plan.Edges {
		if err := ctx.Err(); err != nil {
			status := spec.TaskStatusCanceled
			if !errors.Is(err, context.Canceled) {
				status = spec.TaskStatusFailed
			}
			r.markTaskStopped(ctx, task.ID, request, status, buildExecutionFailureResult(task.ID, request, plan, handoffs, err))
			return nil, err
		}

		taskLogger.Info("dispatching edge", "edge_id", edge.ID, "tool", edge.ToolName, "dependencies", edge.Dependencies)
		handoff, edgeErr := r.scheduler.RunLocalEdge(ctx, EdgeExecutionRequest{
			TaskID:            task.ID,
			EdgeID:            edge.ID,
			ToolName:          edge.ToolName,
			DependencyEdgeIDs: edge.Dependencies,
			SubTaskContent:    edge.SubTask,
		})
		if edgeErr != nil {
			taskLogger.Error("edge execution failed", "edge_id", edge.ID, "tool", edge.ToolName, "error", edgeErr)
			status := spec.TaskStatusFailed
			if errors.Is(edgeErr, context.Canceled) {
				status = spec.TaskStatusCanceled
			}
			r.markTaskStopped(ctx, task.ID, request, status, buildExecutionFailureResult(task.ID, request, plan, handoffs, edgeErr))
			return nil, edgeErr
		}

		taskLogger.Info("edge completed", "edge_id", edge.ID, "tool", edge.ToolName, "status", handoff.Metadata.Status)
		handoffs = append(handoffs, handoff)
	}

	return handoffs, nil
}

func (r *TaskRunner) completeTask(ctx context.Context, taskLogger *slog.Logger, task sqlite.TaskRecord, request string, plan spec.DAGPlan, handoffs []spec.Handoff) (*TaskRunResult, error) {
	task, err := r.finalizeTaskStatus(ctx, task.ID, spec.TaskStatusDone)
	if err != nil {
		return nil, err
	}

	terminal := extractTerminalEdgeResults(plan, handoffs)
	if err := r.tasks.WriteResult(task.ID, buildSuccessResult(task.ID, request, terminal, r.locator)); err != nil {
		return nil, err
	}
	_ = r.tasks.AppendEvent(task.ID, TaskEvent{Timestamp: nowUTC(), Type: "task.run.completed", Message: "task runner finished successfully"})

	taskLogger.Info("task runner completed", "terminal_handoffs", len(terminal), "result_path", r.locator.TaskResultPath(task.ID))

	return &TaskRunResult{
		Task:             task,
		Plan:             plan,
		TerminalHandoffs: metadataOnly(terminal),
		TaskDir:          r.locator.TaskDir(task.ID),
		ResultPath:       r.locator.TaskResultPath(task.ID),
	}, nil
}

func (r *TaskRunner) finalizeTaskStatus(ctx context.Context, taskID string, status spec.TaskStatus) (sqlite.TaskRecord, error) {
	finalizeCtx, cancel := finalizeContext(ctx)
	defer cancel()
	return r.tasks.UpdateStatus(finalizeCtx, taskID, status)
}

func (r *TaskRunner) markTaskStopped(ctx context.Context, taskID, request string, status spec.TaskStatus, result string) {
	finalizeCtx, cancel := finalizeContext(ctx)
	defer cancel()
	_, _ = r.tasks.UpdateStatus(finalizeCtx, taskID, status)
	_ = r.tasks.WriteResult(taskID, result)
}

func extractTerminalEdgeResults(plan spec.DAGPlan, handoffs []spec.Handoff) []terminalEdgeResult {
	if len(plan.Edges) == 0 || len(handoffs) == 0 {
		return nil
	}

	dependencySet := map[string]bool{}
	for _, edge := range plan.Edges {
		for _, dependency := range edge.Dependencies {
			dependencySet[dependency] = true
		}
	}

	handoffByEdge := make(map[string]spec.Handoff, len(handoffs))
	for index, handoff := range handoffs {
		if index < len(plan.Edges) {
			handoffByEdge[plan.Edges[index].ID] = handoff
		}
	}

	var out []terminalEdgeResult
	for _, edge := range plan.Edges {
		if dependencySet[edge.ID] {
			continue
		}
		if handoff, ok := handoffByEdge[edge.ID]; ok {
			out = append(out, terminalEdgeResult{Edge: edge, Handoff: handoff})
		}
	}
	return out
}

func metadataOnly(results []terminalEdgeResult) []spec.HandoffMetadata {
	out := make([]spec.HandoffMetadata, 0, len(results))
	for _, result := range results {
		out = append(out, result.Handoff.Metadata)
	}
	return out
}

func buildSuccessResult(taskID, request string, terminal []terminalEdgeResult, locator *datadir.Locator) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "# Task Result\n\nTask ID: `%s`\n\n", taskID)
	_, _ = fmt.Fprintf(&b, "Request:\n\n%s\n\n", strings.TrimSpace(request))
	_, _ = fmt.Fprintln(&b, "## Terminal Outputs")
	for _, result := range terminal {
		_, _ = fmt.Fprintf(&b, "- Tool `%s` finished with status `%s`\n", result.Handoff.Metadata.Tool, result.Handoff.Metadata.Status)
		_, _ = fmt.Fprintf(&b, "  Output dir: `%s`\n", locator.EdgeOutputDir(taskID, result.Edge.ID))
		_, _ = fmt.Fprintf(&b, "  Private output dir: `%s`\n", locator.EdgePrivateOutputDir(taskID, result.Edge.ID))
		if len(result.Handoff.Metadata.OutputFiles) == 0 {
			_, _ = fmt.Fprintln(&b, "  Files: none declared")
			continue
		}
		_, _ = fmt.Fprintf(&b, "  Files: %s\n", strings.Join(result.Handoff.Metadata.OutputFiles, ", "))
	}
	_, _ = fmt.Fprintf(&b, "\nResult path: `%s`\n", locator.TaskResultPath(taskID))
	return b.String()
}

func buildFailureResult(taskID, request string, err error) string {
	return fmt.Sprintf("# Task Result\n\nTask ID: `%s`\n\nRequest:\n\n%s\n\nStatus: failed during planning\n\nError: %s\n", taskID, strings.TrimSpace(request), err)
}

func buildExecutionFailureResult(taskID, request string, plan spec.DAGPlan, handoffs []spec.Handoff, err error) string {
	return fmt.Sprintf("# Task Result\n\nTask ID: `%s`\n\nRequest:\n\n%s\n\nPlanned stages: `%d`\nCompleted handoffs: `%d`\n\nStatus: failed during execution\n\nError: %s\n", taskID, strings.TrimSpace(request), len(plan.Edges), len(handoffs), err)
}

func nowUTC() time.Time {
	return time.Now().UTC()
}
