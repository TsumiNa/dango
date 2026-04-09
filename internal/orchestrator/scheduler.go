package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/runtime"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
)

// Scheduler executes planned edges and records their runtime state.
type Scheduler struct {
	locator *datadir.Locator
	store   *sqlite.Store
	runtime runtime.ContainerRuntime
	logger  *slog.Logger
}

// EdgeExecutionRequest describes one edge execution request issued by the
// orchestrator.
type EdgeExecutionRequest struct {
	// TaskID identifies the parent task.
	TaskID string
	// EdgeID identifies the edge being executed.
	EdgeID string
	// ToolName identifies the registered tool assigned to the edge.
	ToolName string
	// UpstreamEdgeID points to the producer edge whose output should be mounted.
	UpstreamEdgeID string
	// SubTaskContent is written to sub-task.md before execution when provided.
	SubTaskContent string
}

type edgeExecutionPaths struct {
	subTaskPath string
	inputHost   string
	outputHost  string
}

// NewScheduler constructs the scheduler used to execute demo edges locally.
func NewScheduler(locator *datadir.Locator, store *sqlite.Store, rt runtime.ContainerRuntime, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		locator: locator,
		store:   store,
		runtime: rt,
		logger:  logging.Component(logger, "orchestrator.scheduler"),
	}
}

// RunLocalEdge resolves tool config, prepares local paths, runs the tool, and
// persists the resulting handoff metadata.
func (s *Scheduler) RunLocalEdge(ctx context.Context, request EdgeExecutionRequest) (spec.Handoff, error) {
	edgeLogger := s.logger.With("task_id", request.TaskID, "edge_id", request.EdgeID, "tool", request.ToolName)
	edgeLogger.Info("starting edge execution")

	tool, err := s.loadTool(ctx, edgeLogger, request.ToolName)
	if err != nil {
		return spec.Handoff{}, err
	}

	paths, err := s.prepareEdgePaths(edgeLogger, request)
	if err != nil {
		return spec.Handoff{}, err
	}

	started := time.Now().UTC()
	if err := s.markEdgeRunning(ctx, request, paths.outputHost, started); err != nil {
		edgeLogger.Error("failed to persist running edge state", "error", err)
		return spec.Handoff{}, err
	}

	handoff, frontmatter, err := s.runExecutor(ctx, edgeLogger, request, tool, paths)
	if err != nil {
		s.markEdgeFailed(ctx, edgeLogger, request, paths.outputHost, started, err)
		return spec.Handoff{}, err
	}

	if err := s.markEdgeCompleted(ctx, edgeLogger, request, paths.outputHost, started, handoff, frontmatter); err != nil {
		edgeLogger.Error("failed to persist completed edge state", "error", err)
		return spec.Handoff{}, err
	}

	edgeLogger.Info("edge execution completed", "status", handoff.Metadata.Status, "output_files", handoff.Metadata.OutputFiles)

	return handoff, nil
}

func (s *Scheduler) loadTool(ctx context.Context, edgeLogger *slog.Logger, toolName string) (sqlite.ToolRecord, error) {
	tool, err := s.store.GetTool(ctx, toolName)
	if err != nil {
		edgeLogger.Error("failed to load tool registration", "error", err)
		return sqlite.ToolRecord{}, err
	}

	return tool, nil
}

func (s *Scheduler) prepareEdgePaths(edgeLogger *slog.Logger, request EdgeExecutionRequest) (edgeExecutionPaths, error) {
	if err := s.locator.EnsureEdgeDir(request.TaskID, request.EdgeID); err != nil {
		edgeLogger.Error("failed to ensure edge directory", "error", err)
		return edgeExecutionPaths{}, err
	}

	paths := edgeExecutionPaths{
		subTaskPath: s.locator.EdgeSubTaskPath(request.TaskID, request.EdgeID),
		outputHost:  s.locator.EdgeOutputDir(request.TaskID, request.EdgeID),
	}

	if strings.TrimSpace(request.SubTaskContent) != "" {
		if err := os.WriteFile(paths.subTaskPath, []byte(request.SubTaskContent), 0o644); err != nil {
			edgeLogger.Error("failed to write sub-task", "path", paths.subTaskPath, "error", err)
			return edgeExecutionPaths{}, fmt.Errorf("write sub-task.md: %w", err)
		}
	} else if _, err := os.Stat(paths.subTaskPath); err != nil {
		edgeLogger.Error("missing sub-task for edge execution", "path", paths.subTaskPath, "error", err)
		return edgeExecutionPaths{}, fmt.Errorf("sub-task.md is required before edge execution: %w", err)
	}

	if request.UpstreamEdgeID != "" {
		paths.inputHost = s.locator.EdgeOutputDir(request.TaskID, request.UpstreamEdgeID)
		return paths, nil
	}

	paths.inputHost = s.locator.EdgeScratchInputDir(request.TaskID, request.EdgeID)
	if err := os.MkdirAll(paths.inputHost, 0o755); err != nil {
		edgeLogger.Error("failed to create empty input directory", "path", paths.inputHost, "error", err)
		return edgeExecutionPaths{}, fmt.Errorf("create empty input directory: %w", err)
	}

	return paths, nil
}

func (s *Scheduler) markEdgeRunning(ctx context.Context, request EdgeExecutionRequest, outputHost string, started time.Time) error {
	if err := s.store.UpsertEdge(ctx, sqlite.EdgeRecord{
		ID:        request.EdgeID,
		TaskID:    request.TaskID,
		ToolName:  request.ToolName,
		Upstream:  request.UpstreamEdgeID,
		Status:    string(spec.EdgeStatusRunning),
		SharedDir: outputHost,
		Started:   started.Format(time.RFC3339),
	}); err != nil {
		return err
	}

	return s.store.InsertLog(ctx, request.EdgeID, "info", "starting container execution")
}

func (s *Scheduler) runExecutor(ctx context.Context, edgeLogger *slog.Logger, request EdgeExecutionRequest, tool sqlite.ToolRecord, paths edgeExecutionPaths) (spec.Handoff, []byte, error) {
	runRequest := runtime.ExecutorRunRequest{
		Image:          tool.Image,
		TaskID:         request.TaskID,
		SubTaskHost:    paths.subTaskPath,
		ToolConfigHost: s.locator.ToolMergedPath(request.ToolName),
		InputHost:      paths.inputHost,
		OutputHost:     paths.outputHost,
	}
	edgeLogger.Debug("prepared executor run request", "image", tool.Image, "input_host", paths.inputHost, "output_host", paths.outputHost, "sub_task_path", paths.subTaskPath)
	if err := s.runtime.RunExecutor(ctx, runRequest); err != nil {
		edgeLogger.Error("executor runtime failed", "image", tool.Image, "error", err)
		return spec.Handoff{}, nil, err
	}

	handoffPath := filepath.Join(paths.outputHost, "_handoff.md")
	rawHandoff, err := os.ReadFile(handoffPath)
	if err != nil {
		edgeLogger.Error("failed to read handoff", "path", handoffPath, "error", err)
		return spec.Handoff{}, nil, fmt.Errorf("read handoff file %q: %w", handoffPath, err)
	}

	handoff, err := spec.ParseHandoff(rawHandoff)
	if err != nil {
		edgeLogger.Error("failed to parse handoff", "path", handoffPath, "error", err)
		return spec.Handoff{}, nil, err
	}
	frontmatter, err := spec.ExtractHandoffFrontmatter(rawHandoff)
	if err != nil {
		edgeLogger.Error("failed to extract handoff frontmatter", "path", handoffPath, "error", err)
		return spec.Handoff{}, nil, err
	}

	return handoff, frontmatter, nil
}

func (s *Scheduler) markEdgeFailed(ctx context.Context, edgeLogger *slog.Logger, request EdgeExecutionRequest, outputHost string, started time.Time, runErr error) {
	finalizeCtx, cancel := finalizeContext(ctx)
	defer cancel()

	finished := time.Now().UTC()
	if err := s.store.UpsertEdge(finalizeCtx, sqlite.EdgeRecord{
		ID:        request.EdgeID,
		TaskID:    request.TaskID,
		ToolName:  request.ToolName,
		Upstream:  request.UpstreamEdgeID,
		Status:    string(spec.EdgeStatusFailed),
		SharedDir: outputHost,
		Started:   started.Format(time.RFC3339),
		Finished:  finished.Format(time.RFC3339),
	}); err != nil {
		edgeLogger.Error("failed to persist failed edge state", "error", err)
	}
	if err := s.store.InsertLog(finalizeCtx, request.EdgeID, "error", runErr.Error()); err != nil {
		edgeLogger.Error("failed to write edge failure log", "error", err)
	}
}

func (s *Scheduler) markEdgeCompleted(ctx context.Context, edgeLogger *slog.Logger, request EdgeExecutionRequest, outputHost string, started time.Time, handoff spec.Handoff, frontmatter []byte) error {
	finalizeCtx, cancel := finalizeContext(ctx)
	defer cancel()

	finished := time.Now().UTC()
	if err := s.store.UpsertEdge(finalizeCtx, sqlite.EdgeRecord{
		ID:          request.EdgeID,
		TaskID:      request.TaskID,
		ToolName:    request.ToolName,
		Upstream:    request.UpstreamEdgeID,
		Status:      string(spec.EdgeStatusCompleted),
		SharedDir:   outputHost,
		HandoffYAML: string(frontmatter),
		Started:     started.Format(time.RFC3339),
		Finished:    finished.Format(time.RFC3339),
	}); err != nil {
		return err
	}

	if err := s.store.InsertLog(finalizeCtx, request.EdgeID, "info", "container execution completed"); err != nil {
		edgeLogger.Error("failed to write edge completion log", "error", err)
		return err
	}

	return nil
}
