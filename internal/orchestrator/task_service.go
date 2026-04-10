package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
	"github.com/tsumina/dango/internal/taskflow"
)

// TaskService manages durable task state for the control plane.
//
// A TaskService persists the normalized request, the SQLite task row, the task
// metadata sidecar, append-only lifecycle events, the plan snapshot rendered
// into task.md, and the final result artifact. The runner depends on this
// behavior through the runner.TaskStore interface, while the orchestrator uses
// it directly for create, list, and describe flows. The zero value is not
// usable; callers construct it with [NewTaskService].
type TaskService struct {
	locator *datadir.Locator
	store   *sqlite.Store
	logger  *slog.Logger
}

// NewTaskService constructs the task persistence service shared by the
// orchestrator and runner.
//
// locator defines where task artifacts are written, store defines the SQLite
// task rows, and logger is wrapped with the orchestrator.tasks component name.
func NewTaskService(locator *datadir.Locator, store *sqlite.Store, logger *slog.Logger) *TaskService {
	return &TaskService{
		locator: locator,
		store:   store,
		logger:  logging.Component(logger, "orchestrator.tasks"),
	}
}

// CreateRequest persists a new pending task together with its structured
// request metadata and initial task artifacts.
//
// CreateRequest allocates a new task ID, normalizes the request envelope,
// fills missing request-entry and lineage fields, writes the SQLite task row,
// stores meta.json, appends the initial event, renders task.md, and then
// reloads the task row to return the persisted state. It creates state only;
// it does not start execution.
func (s *TaskService) CreateRequest(ctx context.Context, request taskflow.RequestEnvelope, metadata taskflow.TaskMetadata) (sqlite.TaskRecord, error) {
	taskID, err := spec.NewUUID()
	if err != nil {
		return sqlite.TaskRecord{}, err
	}

	if err := s.locator.EnsureTaskDir(taskID); err != nil {
		return sqlite.TaskRecord{}, err
	}

	metadata.Request = taskflow.NormalizeRequestEnvelope(request)
	if metadata.Entry.ReceivedAt.IsZero() {
		metadata.Entry = taskflow.MergeRequestMetadata(metadata.Entry, taskflow.RequestMetadataFromContext(ctx))
	}
	if metadata.Entry.ReceivedAt.IsZero() {
		metadata.Entry.ReceivedAt = time.Now().UTC()
	}
	if strings.TrimSpace(metadata.Lineage.RootTaskID) == "" {
		metadata.Lineage.RootTaskID = taskID
	}
	if metadata.Lineage.Revision <= 0 {
		metadata.Lineage.Revision = 1
	}

	record := sqlite.TaskRecord{
		ID:      taskID,
		Status:  string(spec.TaskStatusPending),
		Request: taskflow.PrimaryRequestText(metadata.Request),
	}
	if err := s.store.CreateTask(ctx, record); err != nil {
		return sqlite.TaskRecord{}, err
	}
	if err := s.writeMetadata(taskID, metadata); err != nil {
		return sqlite.TaskRecord{}, err
	}
	if err := s.AppendEvent(taskID, taskflow.TaskEvent{
		Timestamp: time.Now().UTC(),
		Type:      "task.created",
		Message:   "task runner created",
		Data: map[string]any{
			"status":   record.Status,
			"revision": metadata.Lineage.Revision,
		},
	}); err != nil {
		return sqlite.TaskRecord{}, err
	}

	taskMarkdown := buildTaskMarkdown(metadata.Request, string(spec.TaskStatusPending), nil)
	if err := os.WriteFile(s.locator.TaskRequestPath(taskID), []byte(taskMarkdown), 0o644); err != nil {
		return sqlite.TaskRecord{}, fmt.Errorf("write task.md: %w", err)
	}

	return s.store.GetTask(ctx, taskID)
}

// Get loads the persisted SQLite task row by ID.
//
// Get does not read metadata, plan snapshots, or events.
func (s *TaskService) Get(ctx context.Context, taskID string) (sqlite.TaskRecord, error) {
	return s.store.GetTask(ctx, taskID)
}

// List loads persisted task summaries together with their metadata and result
// paths.
//
// List combines the row view from SQLite with the sidecar metadata file and
// the stable filesystem locations used for task inspection.
func (s *TaskService) List(ctx context.Context) ([]taskflow.TaskSummary, error) {
	tasks, err := s.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]taskflow.TaskSummary, 0, len(tasks))
	for _, task := range tasks {
		metadata, err := s.loadMetadata(task.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, taskflow.TaskSummary{
			Task:       task,
			Metadata:   metadata,
			TaskDir:    s.locator.TaskDir(task.ID),
			ResultPath: s.locator.TaskResultPath(task.ID),
		})
	}

	return out, nil
}

// Describe returns the fully materialized persisted view for one task.
//
// Describe combines the SQLite task row with the metadata sidecar, decoded
// event log, and any serialized DAG plan so callers can inspect the current
// control-plane state without reconstructing it themselves.
func (s *TaskService) Describe(ctx context.Context, taskID string) (*taskflow.TaskDescription, error) {
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}

	metadata, err := s.loadMetadata(taskID)
	if err != nil {
		return nil, err
	}
	events, err := s.loadEvents(taskID)
	if err != nil {
		return nil, err
	}

	var plan spec.DAGPlan
	if strings.TrimSpace(task.DAGJSON) != "" {
		if err := json.Unmarshal([]byte(task.DAGJSON), &plan); err != nil {
			return nil, fmt.Errorf("decode task plan: %w", err)
		}
	}

	return &taskflow.TaskDescription{
		TaskSummary: taskflow.TaskSummary{
			Task:       task,
			Metadata:   metadata,
			TaskDir:    s.locator.TaskDir(task.ID),
			ResultPath: s.locator.TaskResultPath(task.ID),
		},
		Plan:   plan,
		Events: events,
	}, nil
}

// ApplyPlan persists a plan, updates task status, and rewrites task.md to
// include the current planning view.
//
// Runner code calls ApplyPlan after planning or replanning so the database row,
// task.md artifact, and append-only event log all reflect the same DAG
// revision.
func (s *TaskService) ApplyPlan(ctx context.Context, taskID string, plan spec.DAGPlan, status spec.TaskStatus) (sqlite.TaskRecord, error) {
	payload, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return sqlite.TaskRecord{}, fmt.Errorf("marshal dag plan: %w", err)
	}

	if err := s.store.UpdateTaskPlan(ctx, taskID, string(status), string(payload)); err != nil {
		return sqlite.TaskRecord{}, err
	}

	metadata, err := s.loadMetadata(taskID)
	if err != nil {
		return sqlite.TaskRecord{}, err
	}
	taskMarkdown := buildTaskMarkdown(metadata.Request, string(status), &plan)
	if err := os.WriteFile(s.locator.TaskRequestPath(taskID), []byte(taskMarkdown), 0o644); err != nil {
		return sqlite.TaskRecord{}, fmt.Errorf("write task.md: %w", err)
	}

	_ = s.AppendEvent(taskID, taskflow.TaskEvent{
		Timestamp: time.Now().UTC(),
		Type:      "task.plan.applied",
		Message:   "runner persisted a new DAG revision",
		Data: map[string]any{
			"status":   status,
			"edges":    len(plan.Edges),
			"revision": plan.Revision,
		},
	})

	return s.store.GetTask(ctx, taskID)
}

// UpdateStatus persists a task status transition and appends a matching event.
//
// It returns the reloaded task row so callers can continue the workflow with
// the latest persisted timestamps and state.
func (s *TaskService) UpdateStatus(ctx context.Context, taskID string, status spec.TaskStatus) (sqlite.TaskRecord, error) {
	if err := s.store.UpdateTaskStatus(ctx, taskID, string(status)); err != nil {
		return sqlite.TaskRecord{}, err
	}
	_ = s.AppendEvent(taskID, taskflow.TaskEvent{
		Timestamp: time.Now().UTC(),
		Type:      "task.status.updated",
		Message:   "runner status changed",
		Data: map[string]any{
			"status": status,
		},
	})
	return s.store.GetTask(ctx, taskID)
}

// WriteResult writes the task result artifact and records that write in the
// event log.
//
// The runner calls WriteResult for both successful and explanatory failure
// completions after status transitions have been persisted.
func (s *TaskService) WriteResult(taskID string, result string) error {
	if err := os.WriteFile(s.locator.TaskResultPath(taskID), []byte(result), 0o644); err != nil {
		return fmt.Errorf("write result.md: %w", err)
	}
	return s.AppendEvent(taskID, taskflow.TaskEvent{
		Timestamp: time.Now().UTC(),
		Type:      "task.result.written",
		Message:   "result artifact updated",
		Data: map[string]any{
			"result_path": s.locator.TaskResultPath(taskID),
		},
	})
}

// AppendEvent appends one lifecycle event to the task's JSONL event log.
//
// Events are append-only and are used to reconstruct the visible task history
// in [TaskService.Describe].
func (s *TaskService) AppendEvent(taskID string, event taskflow.TaskEvent) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal task event: %w", err)
	}
	file, err := os.OpenFile(s.locator.TaskEventsPath(taskID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open task events log: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("append task event: %w", err)
	}
	return nil
}

// UpdateMetadata rewrites the structured metadata sidecar for a task.
//
// UpdateMetadata normalizes the request envelope and fills missing lineage or
// timestamp fields before replacing meta.json.
func (s *TaskService) UpdateMetadata(taskID string, metadata taskflow.TaskMetadata) error {
	metadata.Request = taskflow.NormalizeRequestEnvelope(metadata.Request)
	if metadata.Entry.ReceivedAt.IsZero() {
		metadata.Entry.ReceivedAt = time.Now().UTC()
	}
	if metadata.Lineage.Revision <= 0 {
		metadata.Lineage.Revision = 1
	}
	return s.writeMetadata(taskID, metadata)
}

func (s *TaskService) writeMetadata(taskID string, metadata taskflow.TaskMetadata) error {
	payload, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal task metadata: %w", err)
	}
	if err := os.WriteFile(s.locator.TaskMetadataPath(taskID), payload, 0o644); err != nil {
		return fmt.Errorf("write task metadata: %w", err)
	}
	return nil
}

func (s *TaskService) loadMetadata(taskID string) (taskflow.TaskMetadata, error) {
	payload, err := os.ReadFile(s.locator.TaskMetadataPath(taskID))
	if err != nil {
		if os.IsNotExist(err) {
			return taskflow.TaskMetadata{}, nil
		}
		return taskflow.TaskMetadata{}, fmt.Errorf("read task metadata: %w", err)
	}
	var metadata taskflow.TaskMetadata
	if err := json.Unmarshal(payload, &metadata); err != nil {
		return taskflow.TaskMetadata{}, fmt.Errorf("decode task metadata: %w", err)
	}
	return metadata, nil
}

func (s *TaskService) loadEvents(taskID string) ([]taskflow.TaskEvent, error) {
	file, err := os.Open(s.locator.TaskEventsPath(taskID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open task events: %w", err)
	}
	defer file.Close()

	var events []taskflow.TaskEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event taskflow.TaskEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("decode task event: %w", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan task events: %w", err)
	}
	return events, nil
}

func buildTaskMarkdown(request taskflow.RequestEnvelope, status string, plan *spec.DAGPlan) string {
	requestText := taskflow.PrimaryRequestText(request)
	if requestText == "" {
		requestText = "(empty request)"
	}

	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "# Task Request\n\n%s\n\n# Runner Status\n\nStatus: %s\n\n", requestText, status)

	if len(request.Parts) > 0 {
		_, _ = fmt.Fprintln(&b, "## Request Parts")
		for index, part := range request.Parts {
			label := part.Kind
			if label == "" {
				label = "part"
			}
			if part.Name != "" {
				label += " (" + part.Name + ")"
			}
			_, _ = fmt.Fprintf(&b, "%d. %s\n", index+1, label)
		}
		_, _ = fmt.Fprintln(&b)
	}

	if plan == nil || len(plan.Edges) == 0 {
		_, _ = fmt.Fprint(&b, "The task runner has not persisted a DAG yet.\n")
		return b.String()
	}

	_, _ = fmt.Fprintf(&b, "Planner: %s\nMode: %s\nRevision: %d\nCreated: %s\n", plan.Planner, plan.Mode, plan.Revision, plan.CreatedAt.Format(time.RFC3339))
	if !plan.ReviewedAt.IsZero() {
		_, _ = fmt.Fprintf(&b, "Reviewed: %s\n", plan.ReviewedAt.Format(time.RFC3339))
	}
	_, _ = fmt.Fprintln(&b)
	_, _ = fmt.Fprintln(&b, "## Edges")
	for index, edge := range plan.Edges {
		dependencies := "(root)"
		if len(edge.Dependencies) > 0 {
			dependencies = strings.Join(edge.Dependencies, ", ")
		}
		_, _ = fmt.Fprintf(&b, "%d. `%s` `%s -> %s` deps=%s\n", index+1, edge.ToolName, edge.InputType, edge.OutputType, dependencies)
		if strings.TrimSpace(edge.Summary) != "" {
			_, _ = fmt.Fprintf(&b, "   %s\n", strings.TrimSpace(edge.Summary))
		}
	}

	return b.String()
}
