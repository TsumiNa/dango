package runner

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
	"github.com/tsumina/dango/internal/taskflow"
)

type terminalEdgeResult struct {
	Edge    spec.PlannedEdge
	Handoff spec.Handoff
}

// TaskRunner executes the full lifecycle of one persisted task.
//
// A TaskRunner turns a pending task row plus its metadata into a terminal
// result by coordinating planning, durable status transitions, edge execution,
// and finalization. It depends on TaskStore for persistence, PlanBuilder for
// DAG construction, and Scheduler for per-edge execution. The zero value is not
// usable; callers construct it with [NewTaskRunner].
type TaskRunner struct {
	locator   *datadir.Locator
	tasks     TaskStore
	planner   PlanBuilder
	scheduler *Scheduler
	logger    *slog.Logger
	task      sqlite.TaskRecord
	metadata  taskflow.TaskMetadata
}

// NewTaskRunner constructs a runner for one already-persisted task.
//
// Callers are expected to pass the task row and metadata returned by the
// control-plane persistence layer rather than constructing those values by hand.
func NewTaskRunner(locator *datadir.Locator, tasks TaskStore, planner PlanBuilder, scheduler *Scheduler, task sqlite.TaskRecord, metadata taskflow.TaskMetadata, logger *slog.Logger) *TaskRunner {
	return &TaskRunner{
		locator:   locator,
		tasks:     tasks,
		planner:   planner,
		scheduler: scheduler,
		logger:    logging.Component(logger, "runner.task_runner"),
		task:      task,
		metadata:  metadata,
	}
}

// Run executes the task's end-to-end lifecycle and returns the terminal result.
//
// The workflow is: derive the request text, append the start event, plan the
// task, persist and execute the reviewed DAG, finalize the terminal status, and
// write the final result artifact. Planning or execution failures are converted
// into persisted failure or canceled state before the error is returned.
func (r *TaskRunner) Run(ctx context.Context) (*taskflow.TaskRunResult, error) {
	if r.planner == nil {
		return nil, fmt.Errorf("runner planner is required")
	}

	requestEnvelope := r.metadata.Request
	request := taskflow.PrimaryRequestText(requestEnvelope)
	if strings.TrimSpace(request) == "" {
		request = r.task.Request
		if strings.TrimSpace(requestEnvelope.Text) == "" {
			requestEnvelope.Text = request
		}
	}
	taskLogger := r.logger.With("task_id", r.task.ID)
	_ = r.tasks.AppendEvent(r.task.ID, taskflow.TaskEvent{Timestamp: nowUTC(), Type: "task.run.started", Message: "task runner execution started"})

	task, plan, err := r.planTask(ctx, taskLogger, requestEnvelope, request)
	if err != nil {
		return nil, err
	}

	results, err := r.executePlan(ctx, taskLogger, task, request, plan)
	if err != nil {
		return nil, err
	}

	return r.completeTask(ctx, taskLogger, task, request, plan, results)
}

func (r *TaskRunner) planTask(ctx context.Context, taskLogger *slog.Logger, requestEnvelope taskflow.RequestEnvelope, request string) (sqlite.TaskRecord, spec.DAGPlan, error) {
	task, err := r.tasks.UpdateStatus(ctx, r.task.ID, spec.TaskStatusPlanning)
	if err != nil {
		return sqlite.TaskRecord{}, spec.DAGPlan{}, err
	}

	plan, err := r.planner.Plan(ctx, task.ID, requestEnvelope)
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

func (r *TaskRunner) executePlan(ctx context.Context, taskLogger *slog.Logger, task sqlite.TaskRecord, request string, plan spec.DAGPlan) ([]EdgeResult, error) {
	stateMachine := NewStateMachine(r.scheduler, taskLogger)
	results, err := stateMachine.Run(ctx, task.ID, plan)
	if err != nil {
		status := spec.TaskStatusFailed
		if errors.Is(err, context.Canceled) {
			status = spec.TaskStatusCanceled
		}
		r.markTaskStopped(ctx, task.ID, request, status, buildExecutionFailureResult(task.ID, request, plan, results, err))
		return nil, err
	}
	return results, nil
}

func (r *TaskRunner) completeTask(ctx context.Context, taskLogger *slog.Logger, task sqlite.TaskRecord, request string, plan spec.DAGPlan, results []EdgeResult) (*taskflow.TaskRunResult, error) {
	task, err := r.finalizeTaskStatus(ctx, task.ID, spec.TaskStatusDone)
	if err != nil {
		return nil, err
	}

	terminal := extractTerminalEdgeResults(plan, results)
	if err := r.tasks.WriteResult(task.ID, buildSuccessResult(task.ID, request, terminal, r.locator)); err != nil {
		return nil, err
	}
	_ = r.tasks.AppendEvent(task.ID, taskflow.TaskEvent{Timestamp: nowUTC(), Type: "task.run.completed", Message: "task runner finished successfully"})

	taskLogger.Info("task runner completed", "terminal_handoffs", len(terminal), "result_path", r.locator.TaskResultPath(task.ID))

	return &taskflow.TaskRunResult{
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

func extractTerminalEdgeResults(plan spec.DAGPlan, results []EdgeResult) []terminalEdgeResult {
	if len(plan.Edges) == 0 || len(results) == 0 {
		return nil
	}

	dependencySet := map[string]bool{}
	for _, edge := range plan.Edges {
		for _, dependency := range edge.Dependencies {
			dependencySet[dependency] = true
		}
	}

	handoffByEdge := make(map[string]spec.Handoff, len(results))
	for _, result := range results {
		handoffByEdge[result.EdgeID] = result.Handoff
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

func buildExecutionFailureResult(taskID, request string, plan spec.DAGPlan, results []EdgeResult, err error) string {
	return fmt.Sprintf("# Task Result\n\nTask ID: `%s`\n\nRequest:\n\n%s\n\nPlanned stages: `%d`\nCompleted handoffs: `%d`\n\nStatus: failed during execution\n\nError: %s\n", taskID, strings.TrimSpace(request), len(plan.Edges), len(results), err)
}

func nowUTC() time.Time {
	return time.Now().UTC()
}
