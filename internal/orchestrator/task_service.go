package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
)

// TaskService manages task lifecycle persistence and task markdown artifacts.
type TaskService struct {
	locator *datadir.Locator
	store   *sqlite.Store
	logger  *slog.Logger
}

// NewTaskService constructs the task persistence service.
func NewTaskService(locator *datadir.Locator, store *sqlite.Store, logger *slog.Logger) *TaskService {
	return &TaskService{
		locator: locator,
		store:   store,
		logger:  logging.Component(logger, "orchestrator.tasks"),
	}
}

// Create allocates a task ID, persists the initial task row, and writes task.md.
func (s *TaskService) Create(ctx context.Context, request string) (sqlite.TaskRecord, error) {
	taskID, err := spec.NewUUID()
	if err != nil {
		return sqlite.TaskRecord{}, err
	}

	if err := s.locator.EnsureTaskDir(taskID); err != nil {
		return sqlite.TaskRecord{}, err
	}

	record := sqlite.TaskRecord{
		ID:      taskID,
		Status:  string(spec.TaskStatusPlanning),
		Request: request,
	}
	s.logger.Info("creating task", "task_id", taskID, "status", record.Status)
	if err := s.store.CreateTask(ctx, record); err != nil {
		s.logger.Error("failed to create task record", "task_id", taskID, "error", err)
		return sqlite.TaskRecord{}, err
	}

	taskMarkdown := buildTaskMarkdown(request, string(spec.TaskStatusPlanning), nil)
	if err := os.WriteFile(s.locator.TaskRequestPath(taskID), []byte(taskMarkdown), 0o644); err != nil {
		s.logger.Error("failed to write task markdown", "task_id", taskID, "error", err)
		return sqlite.TaskRecord{}, fmt.Errorf("write task.md: %w", err)
	}

	s.logger.Debug("task created", "task_id", taskID, "task_path", s.locator.TaskRequestPath(taskID))
	return s.store.GetTask(ctx, taskID)
}

// Get loads a task row by ID.
func (s *TaskService) Get(ctx context.Context, taskID string) (sqlite.TaskRecord, error) {
	return s.store.GetTask(ctx, taskID)
}

// ApplyPlan persists a plan, updates task status, and rewrites task.md to
// include the current planning view.
func (s *TaskService) ApplyPlan(ctx context.Context, taskID string, plan spec.DAGPlan, status spec.TaskStatus) (sqlite.TaskRecord, error) {
	payload, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		s.logger.Error("failed to marshal task plan", "task_id", taskID, "error", err)
		return sqlite.TaskRecord{}, fmt.Errorf("marshal dag plan: %w", err)
	}

	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return sqlite.TaskRecord{}, err
	}

	if err := s.store.UpdateTaskPlan(ctx, taskID, string(status), string(payload)); err != nil {
		s.logger.Error("failed to persist task plan", "task_id", taskID, "status", status, "error", err)
		return sqlite.TaskRecord{}, err
	}

	taskMarkdown := buildTaskMarkdown(task.Request, string(status), &plan)
	if err := os.WriteFile(s.locator.TaskRequestPath(taskID), []byte(taskMarkdown), 0o644); err != nil {
		s.logger.Error("failed to write planned task markdown", "task_id", taskID, "error", err)
		return sqlite.TaskRecord{}, fmt.Errorf("write task.md: %w", err)
	}

	s.logger.Info("task plan applied", "task_id", taskID, "status", status, "edges", len(plan.Edges))
	return s.store.GetTask(ctx, taskID)
}

// UpdateStatus persists a task status transition.
func (s *TaskService) UpdateStatus(ctx context.Context, taskID string, status spec.TaskStatus) (sqlite.TaskRecord, error) {
	s.logger.Info("updating task status", "task_id", taskID, "status", status)
	if err := s.store.UpdateTaskStatus(ctx, taskID, string(status)); err != nil {
		s.logger.Error("failed to update task status", "task_id", taskID, "status", status, "error", err)
		return sqlite.TaskRecord{}, err
	}
	return s.store.GetTask(ctx, taskID)
}

// WriteResult writes the final result.md artifact for a task.
func (s *TaskService) WriteResult(taskID string, result string) error {
	if err := os.WriteFile(s.locator.TaskResultPath(taskID), []byte(result), 0o644); err != nil {
		s.logger.Error("failed to write task result", "task_id", taskID, "error", err)
		return fmt.Errorf("write result.md: %w", err)
	}
	s.logger.Info("task result written", "task_id", taskID, "result_path", s.locator.TaskResultPath(taskID))
	return nil
}

func buildTaskMarkdown(request string, status string, plan *spec.DAGPlan) string {
	request = strings.TrimSpace(request)
	if request == "" {
		request = "(empty request)"
	}

	var b strings.Builder
	_, _ = fmt.Fprintf(&b, `# Task Request

%s

# Planning

Status: %s

`, request, status)

	if plan == nil || len(plan.Edges) == 0 {
		_, _ = fmt.Fprint(&b, "The orchestration planner/DAG generator is not implemented yet.\n")
		return b.String()
	}

	_, _ = fmt.Fprintf(&b, "Planner: %s\nMode: %s\nCreated: %s\n\n", plan.Planner, plan.Mode, plan.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	_, _ = fmt.Fprintln(&b, "## Edges")
	for index, edge := range plan.Edges {
		upstream := edge.Upstream
		if upstream == "" {
			upstream = "(root)"
		}
		_, _ = fmt.Fprintf(&b, "%d. `%s`  `%s -> %s`  upstream=%s\n", index+1, edge.ToolName, edge.InputType, edge.OutputType, upstream)
	}

	return b.String()
}
