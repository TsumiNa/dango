package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/spec"
)

type runnerHandle struct {
	cancel context.CancelFunc
}

// TaskRunnerService manages runner creation, lookup, and control operations.
type TaskRunnerService struct {
	locator   *datadir.Locator
	tasks     *TaskService
	planner   *Planner
	scheduler *Scheduler
	logger    *slog.Logger

	mu      sync.Mutex
	runners map[string]runnerHandle
}

// NewTaskRunnerService constructs the orchestrator-facing runner control plane.
func NewTaskRunnerService(locator *datadir.Locator, tasks *TaskService, planner *Planner, scheduler *Scheduler, logger *slog.Logger) *TaskRunnerService {
	return &TaskRunnerService{
		locator:   locator,
		tasks:     tasks,
		planner:   planner,
		scheduler: scheduler,
		logger:    logging.Component(logger, "orchestrator.task_runner_service"),
		runners:   map[string]runnerHandle{},
	}
}

// Create persists a pending task runner without starting execution.
func (s *TaskRunnerService) Create(ctx context.Context, request RequestEnvelope) (*TaskDescription, error) {
	task, err := s.tasks.CreateRequest(ctx, request, TaskMetadata{Entry: RequestMetadataFromContext(ctx)})
	if err != nil {
		return nil, err
	}
	return s.tasks.Describe(ctx, task.ID)
}

// RunNow creates a task runner and executes it synchronously in the caller context.
func (s *TaskRunnerService) RunNow(ctx context.Context, request RequestEnvelope) (*TaskRunResult, error) {
	description, err := s.Create(ctx, request)
	if err != nil {
		return nil, err
	}
	return s.runWithDescription(ctx, description)
}

// List returns persisted task summaries.
func (s *TaskRunnerService) List(ctx context.Context) ([]TaskSummary, error) {
	return s.tasks.List(ctx)
}

// Describe returns the persisted description for one task runner.
func (s *TaskRunnerService) Describe(ctx context.Context, taskID string) (*TaskDescription, error) {
	return s.tasks.Describe(ctx, taskID)
}

// Resume starts a pending or paused runner in the background.
func (s *TaskRunnerService) Resume(ctx context.Context, taskID string) (*TaskDescription, error) {
	description, err := s.tasks.Describe(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !canResumeStatus(description.Task.Status) {
		return description, nil
	}
	if s.isRunning(taskID) {
		return description, nil
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s.register(taskID, cancel)
	go func() {
		defer s.unregister(taskID)
		if _, err := s.runWithDescription(runCtx, description); err != nil {
			s.logger.Error("background task runner failed", "task_id", taskID, "error", err)
		}
	}()

	_ = s.tasks.AppendEvent(taskID, TaskEvent{Timestamp: nowUTC(), Type: "task.run.resumed", Message: "task runner resumed in background"})
	return s.tasks.Describe(ctx, taskID)
}

// Cancel marks a runner as canceled and stops any active in-memory execution.
func (s *TaskRunnerService) Cancel(ctx context.Context, taskID string) (*TaskDescription, error) {
	s.mu.Lock()
	handle, ok := s.runners[taskID]
	s.mu.Unlock()
	if ok && handle.cancel != nil {
		handle.cancel()
	}
	if _, err := s.tasks.UpdateStatus(ctx, taskID, spec.TaskStatusCanceled); err != nil {
		return nil, err
	}
	_ = s.tasks.AppendEvent(taskID, TaskEvent{Timestamp: nowUTC(), Type: "task.run.canceled", Message: "task runner canceled by orchestrator"})
	return s.tasks.Describe(ctx, taskID)
}

// Clone creates a new pending runner that preserves the request and plan lineage.
func (s *TaskRunnerService) Clone(ctx context.Context, taskID string, reason string) (*TaskDescription, error) {
	source, err := s.tasks.Describe(ctx, taskID)
	if err != nil {
		return nil, err
	}

	revision := source.Metadata.Lineage.Revision + 1
	metadata := TaskMetadata{
		Request: source.Metadata.Request,
		Entry:   mergeRequestMetadata(RequestMetadataFromContext(ctx), source.Metadata.Entry),
		Lineage: TaskLineage{
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
	_ = s.tasks.AppendEvent(task.ID, TaskEvent{Timestamp: nowUTC(), Type: "task.cloned", Message: "task runner cloned from prior lineage", Data: map[string]any{"source_task_id": source.Task.ID, "reason": reason}})
	_ = s.tasks.AppendEvent(source.Task.ID, TaskEvent{Timestamp: nowUTC(), Type: "task.clone.created", Message: "task runner was cloned", Data: map[string]any{"cloned_task_id": task.ID, "reason": reason}})
	return s.tasks.Describe(ctx, task.ID)
}

func (s *TaskRunnerService) runWithDescription(ctx context.Context, description *TaskDescription) (*TaskRunResult, error) {
	metadata := description.Metadata
	if strings.TrimSpace(metadata.Lineage.RootTaskID) == "" {
		metadata.Lineage.RootTaskID = description.Task.ID
	}
	runner := NewTaskRunner(s.locator, s.tasks, s.planner, s.scheduler, description.Task, metadata, s.logger)

	runCtx, cancel := context.WithCancel(ctx)
	s.register(description.Task.ID, cancel)
	defer s.unregister(description.Task.ID)
	return runner.Run(runCtx)
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
