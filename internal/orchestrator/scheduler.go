package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/layout"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/runtime"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
)

type Scheduler struct {
	layout  *layout.Layout
	store   *sqlite.Store
	runtime runtime.ContainerRuntime
	logger  *slog.Logger
}

type EdgeExecutionRequest struct {
	TaskID         string
	EdgeID         string
	ToolName       string
	UpstreamEdgeID string
	SubTaskContent string
}

func NewScheduler(layout *layout.Layout, store *sqlite.Store, rt runtime.ContainerRuntime, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		layout:  layout,
		store:   store,
		runtime: rt,
		logger:  logging.Component(logger, "orchestrator.scheduler"),
	}
}

func (s *Scheduler) RunLocalEdge(ctx context.Context, request EdgeExecutionRequest) (spec.Handoff, error) {
	edgeLogger := s.logger.With("task_id", request.TaskID, "edge_id", request.EdgeID, "tool", request.ToolName)
	edgeLogger.Info("starting edge execution")
	if err := s.layout.EnsureEdgeDir(request.TaskID, request.EdgeID); err != nil {
		edgeLogger.Error("failed to ensure edge directory", "error", err)
		return spec.Handoff{}, err
	}

	tool, err := s.store.GetTool(ctx, request.ToolName)
	if err != nil {
		edgeLogger.Error("failed to load tool registration", "error", err)
		return spec.Handoff{}, err
	}

	subTaskPath := s.layout.EdgeSubTaskPath(request.TaskID, request.EdgeID)
	if strings.TrimSpace(request.SubTaskContent) != "" {
		if err := os.WriteFile(subTaskPath, []byte(request.SubTaskContent), 0o644); err != nil {
			edgeLogger.Error("failed to write sub-task", "path", subTaskPath, "error", err)
			return spec.Handoff{}, fmt.Errorf("write sub-task.md: %w", err)
		}
	} else if _, err := os.Stat(subTaskPath); err != nil {
		edgeLogger.Error("missing sub-task for edge execution", "path", subTaskPath, "error", err)
		return spec.Handoff{}, fmt.Errorf("sub-task.md is required before edge execution: %w", err)
	}

	inputHost := ""
	if request.UpstreamEdgeID != "" {
		inputHost = s.layout.EdgeOutputDir(request.TaskID, request.UpstreamEdgeID)
	} else {
		inputHost = s.layout.EdgeScratchInputDir(request.TaskID, request.EdgeID)
		if err := os.MkdirAll(inputHost, 0o755); err != nil {
			edgeLogger.Error("failed to create empty input directory", "path", inputHost, "error", err)
			return spec.Handoff{}, fmt.Errorf("create empty input directory: %w", err)
		}
	}

	outputHost := s.layout.EdgeOutputDir(request.TaskID, request.EdgeID)
	started := time.Now().UTC()

	edgeRecord := sqlite.EdgeRecord{
		ID:        request.EdgeID,
		TaskID:    request.TaskID,
		ToolName:  request.ToolName,
		Upstream:  request.UpstreamEdgeID,
		Status:    string(spec.EdgeStatusRunning),
		SharedDir: outputHost,
		Started:   started.Format(time.RFC3339),
	}
	if err := s.store.UpsertEdge(ctx, edgeRecord); err != nil {
		edgeLogger.Error("failed to persist running edge state", "error", err)
		return spec.Handoff{}, err
	}

	if err := s.store.InsertLog(ctx, request.EdgeID, "info", "starting container execution"); err != nil {
		edgeLogger.Error("failed to write edge start log", "error", err)
		return spec.Handoff{}, err
	}

	runRequest := runtime.ExecutorRunRequest{
		Image:          tool.Image,
		TaskID:         request.TaskID,
		SubTaskHost:    subTaskPath,
		ToolConfigHost: s.layout.ToolMergedPath(request.ToolName),
		InputHost:      inputHost,
		OutputHost:     outputHost,
	}
	edgeLogger.Debug("prepared executor run request", "image", tool.Image, "input_host", inputHost, "output_host", outputHost, "sub_task_path", subTaskPath)
	if err := s.runtime.RunExecutor(ctx, runRequest); err != nil {
		finished := time.Now().UTC()
		edgeLogger.Error("executor runtime failed", "image", tool.Image, "error", err)
		_ = s.store.UpsertEdge(ctx, sqlite.EdgeRecord{
			ID:        request.EdgeID,
			TaskID:    request.TaskID,
			ToolName:  request.ToolName,
			Upstream:  request.UpstreamEdgeID,
			Status:    string(spec.EdgeStatusFailed),
			SharedDir: outputHost,
			Started:   started.Format(time.RFC3339),
			Finished:  finished.Format(time.RFC3339),
		})
		_ = s.store.InsertLog(ctx, request.EdgeID, "error", err.Error())
		return spec.Handoff{}, err
	}

	handoffPath := filepath.Join(outputHost, "_handoff.md")
	rawHandoff, err := os.ReadFile(handoffPath)
	if err != nil {
		edgeLogger.Error("failed to read handoff", "path", handoffPath, "error", err)
		return spec.Handoff{}, fmt.Errorf("read handoff file %q: %w", handoffPath, err)
	}

	handoff, err := spec.ParseHandoff(rawHandoff)
	if err != nil {
		edgeLogger.Error("failed to parse handoff", "path", handoffPath, "error", err)
		return spec.Handoff{}, err
	}
	frontmatter, err := spec.ExtractHandoffFrontmatter(rawHandoff)
	if err != nil {
		edgeLogger.Error("failed to extract handoff frontmatter", "path", handoffPath, "error", err)
		return spec.Handoff{}, err
	}

	finished := time.Now().UTC()
	if err := s.store.UpsertEdge(ctx, sqlite.EdgeRecord{
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
		edgeLogger.Error("failed to persist completed edge state", "error", err)
		return spec.Handoff{}, err
	}

	if err := s.store.InsertLog(ctx, request.EdgeID, "info", "container execution completed"); err != nil {
		edgeLogger.Error("failed to write edge completion log", "error", err)
		return spec.Handoff{}, err
	}

	edgeLogger.Info("edge execution completed", "status", handoff.Metadata.Status, "output_files", handoff.Metadata.OutputFiles)

	return handoff, nil
}
