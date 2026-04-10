package runner

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
	"github.com/tsumina/dango/internal/taskflow"
)

// PlanBuilder derives the reviewed executable DAG plan for one persisted task.
//
// TaskRunnerService and TaskRunner depend on this interface so they can drive
// planning without depending on a concrete planner implementation.
type PlanBuilder interface {
	Plan(ctx context.Context, taskID string, request taskflow.RequestEnvelope) (spec.DAGPlan, error)
}

// TaskStore persists the task state and artifacts that the runner mutates while
// executing a task.
//
// The orchestrator's TaskService is the primary implementation. The interface
// is intentionally narrow so the runner depends only on the persistence
// operations it actually needs.
type TaskStore interface {
	CreateRequest(ctx context.Context, request taskflow.RequestEnvelope, metadata taskflow.TaskMetadata) (sqlite.TaskRecord, error)
	List(ctx context.Context) ([]taskflow.TaskSummary, error)
	Describe(ctx context.Context, taskID string) (*taskflow.TaskDescription, error)
	UpdateStatus(ctx context.Context, taskID string, status spec.TaskStatus) (sqlite.TaskRecord, error)
	ApplyPlan(ctx context.Context, taskID string, plan spec.DAGPlan, status spec.TaskStatus) (sqlite.TaskRecord, error)
	AppendEvent(taskID string, event taskflow.TaskEvent) error
	WriteResult(taskID string, result string) error
}

type runnerHandle struct {
	cancel context.CancelFunc
}

// TaskRunnerService is the orchestrator-facing facade for runner control.
//
// It creates pending tasks, starts or resumes background execution, runs tasks
// synchronously when requested, exposes persisted descriptions, tracks active
// in-memory cancellations, and handles clone or cancel operations. The service
// depends on TaskStore for persistence, PlanBuilder for planning, and Scheduler
// for edge execution. The zero value is not usable; callers construct it with
// [NewTaskRunnerService].
type TaskRunnerService struct {
	locator   *datadir.Locator
	tasks     TaskStore
	planner   PlanBuilder
	scheduler *Scheduler
	logger    *slog.Logger

	mu      sync.Mutex
	runners map[string]runnerHandle
}

// NewTaskRunnerService constructs the runner control service used by the
// orchestrator.
//
// The returned service is safe to call concurrently. It does not begin any
// execution until [TaskRunnerService.Start], [TaskRunnerService.Resume], or
// [TaskRunnerService.RunNow] is called.
func NewTaskRunnerService(locator *datadir.Locator, tasks TaskStore, planner PlanBuilder, scheduler *Scheduler, logger *slog.Logger) *TaskRunnerService {
	return &TaskRunnerService{
		locator:   locator,
		tasks:     tasks,
		planner:   planner,
		scheduler: scheduler,
		logger:    logging.Component(logger, "runner.service"),
		runners:   map[string]runnerHandle{},
	}
}

// Start creates a pending task and starts its runner in the background.
//
// Start returns once the task has been persisted and the background goroutine
// has been scheduled. The returned description reflects durable state, not task
// completion.
func (s *TaskRunnerService) Start(ctx context.Context, request taskflow.RequestEnvelope) (*taskflow.TaskDescription, error) {
	description, err := s.Create(ctx, request)
	if err != nil {
		return nil, err
	}
	return s.startWithDescription(ctx, description, "task.run.started", "task runner started in background")
}

// Create persists a pending task without starting execution.
//
// Create is the shared first step for both Start and RunNow.
func (s *TaskRunnerService) Create(ctx context.Context, request taskflow.RequestEnvelope) (*taskflow.TaskDescription, error) {
	task, err := s.tasks.CreateRequest(ctx, request, taskflow.TaskMetadata{Entry: taskflow.RequestMetadataFromContext(ctx)})
	if err != nil {
		return nil, err
	}
	return s.tasks.Describe(ctx, task.ID)
}

// RunNow creates a task and executes its runner synchronously in the caller's
// context.
//
// RunNow is primarily used by tests and CLI flows that need the terminal result
// before returning.
func (s *TaskRunnerService) RunNow(ctx context.Context, request taskflow.RequestEnvelope) (*taskflow.TaskRunResult, error) {
	description, err := s.Create(ctx, request)
	if err != nil {
		return nil, err
	}
	return s.runWithDescription(ctx, description)
}

// List returns the persisted task summaries exposed by the backing TaskStore.
func (s *TaskRunnerService) List(ctx context.Context) ([]taskflow.TaskSummary, error) {
	return s.tasks.List(ctx)
}

// Describe returns the current persisted description for one task.
func (s *TaskRunnerService) Describe(ctx context.Context, taskID string) (*taskflow.TaskDescription, error) {
	return s.tasks.Describe(ctx, taskID)
}

// Resume starts a resumable task in the background when its status allows it.
//
// If the task is already in a non-resumable state, Resume returns the current
// description without mutating execution state.
func (s *TaskRunnerService) Resume(ctx context.Context, taskID string) (*taskflow.TaskDescription, error) {
	description, err := s.tasks.Describe(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !canResumeStatus(description.Task.Status) {
		return description, nil
	}
	return s.startWithDescription(ctx, description, "task.run.resumed", "task runner resumed in background")
}

func (s *TaskRunnerService) startWithDescription(ctx context.Context, description *taskflow.TaskDescription, eventType string, message string) (*taskflow.TaskDescription, error) {
	taskID := description.Task.ID
	if s.isRunning(taskID) {
		return description, nil
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s.register(taskID, cancel)
	go func() {
		defer s.unregister(taskID)
		if _, err := s.runTask(runCtx, description); err != nil {
			s.logger.Error("background task runner failed", "task_id", taskID, "error", err)
		}
	}()

	_ = s.tasks.AppendEvent(taskID, taskflow.TaskEvent{Timestamp: nowUTC(), Type: eventType, Message: message})
	return s.tasks.Describe(ctx, taskID)
}

// Cancel stops any active in-memory execution and persists the canceled state.
//
// Cancel is best-effort with respect to live goroutines: it cancels the active
// context when present, then records the durable canceled status regardless of
// whether a runner handle existed.
func (s *TaskRunnerService) Cancel(ctx context.Context, taskID string) (*taskflow.TaskDescription, error) {
	s.mu.Lock()
	handle, ok := s.runners[taskID]
	s.mu.Unlock()
	if ok && handle.cancel != nil {
		handle.cancel()
	}
	if _, err := s.tasks.UpdateStatus(ctx, taskID, spec.TaskStatusCanceled); err != nil {
		return nil, err
	}
	_ = s.tasks.AppendEvent(taskID, taskflow.TaskEvent{Timestamp: nowUTC(), Type: "task.run.canceled", Message: "task runner canceled by orchestrator"})
	return s.tasks.Describe(ctx, taskID)
}

// Clone creates a new pending task that preserves the source request and
// lineage.
//
// When the source task already has a plan, Clone copies that plan onto the new
// task with an incremented revision so the new lineage can be reviewed, edited,
// or resumed independently.
func (s *TaskRunnerService) Clone(ctx context.Context, taskID string, reason string) (*taskflow.TaskDescription, error) {
	source, err := s.tasks.Describe(ctx, taskID)
	if err != nil {
		return nil, err
	}

	revision := source.Metadata.Lineage.Revision + 1
	metadata := taskflow.TaskMetadata{
		Request: source.Metadata.Request,
		Entry:   taskflow.MergeRequestMetadata(taskflow.RequestMetadataFromContext(ctx), source.Metadata.Entry),
		Lineage: taskflow.TaskLineage{
			RootTaskID:    source.Metadata.Lineage.RootTaskID,
			ParentTaskID:  source.Task.ID,
			CloneOfTaskID: source.Task.ID,
			Revision:      revision,
		},
	}
	if strings.TrimSpace(metadata.Lineage.RootTaskID) == "" {
		metadata.Lineage.RootTaskID = source.Task.ID
	}

	task, err := s.tasks.CreateRequest(ctx, source.Metadata.Request, metadata)
	if err != nil {
		return nil, err
	}
	if len(source.Plan.Edges) > 0 {
		plan := source.Plan
		plan.Revision = revision
		if _, err := s.tasks.ApplyPlan(ctx, task.ID, plan, spec.TaskStatusPending); err != nil {
			return nil, err
		}
	}
	_ = s.tasks.AppendEvent(task.ID, taskflow.TaskEvent{Timestamp: nowUTC(), Type: "task.cloned", Message: "task runner cloned from prior lineage", Data: map[string]any{"source_task_id": source.Task.ID, "reason": reason}})
	_ = s.tasks.AppendEvent(source.Task.ID, taskflow.TaskEvent{Timestamp: nowUTC(), Type: "task.clone.created", Message: "task runner was cloned", Data: map[string]any{"cloned_task_id": task.ID, "reason": reason}})
	return s.tasks.Describe(ctx, task.ID)
}

func (s *TaskRunnerService) runWithDescription(ctx context.Context, description *taskflow.TaskDescription) (*taskflow.TaskRunResult, error) {
	runCtx, cancel := context.WithCancel(ctx)
	s.register(description.Task.ID, cancel)
	defer s.unregister(description.Task.ID)
	return s.runTask(runCtx, description)
}

func (s *TaskRunnerService) runTask(ctx context.Context, description *taskflow.TaskDescription) (*taskflow.TaskRunResult, error) {
	metadata := description.Metadata
	if strings.TrimSpace(metadata.Lineage.RootTaskID) == "" {
		metadata.Lineage.RootTaskID = description.Task.ID
	}
	runner := NewTaskRunner(s.locator, s.tasks, s.planner, s.scheduler, description.Task, metadata, s.logger)
	return runner.Run(ctx)
}

func (s *TaskRunnerService) register(taskID string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runners[taskID] = runnerHandle{cancel: cancel}
}

func (s *TaskRunnerService) unregister(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runners, taskID)
}

func (s *TaskRunnerService) isRunning(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.runners[taskID]
	return ok
}

func canResumeStatus(status string) bool {
	switch spec.TaskStatus(status) {
	case spec.TaskStatusPending, spec.TaskStatusPaused, spec.TaskStatusCanceled:
		return true
	default:
		return false
	}
}
