package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/spec"
	"gopkg.in/yaml.v3"
)

type Executor struct {
	stdout io.Writer
	stderr io.Writer
	logger *slog.Logger
}

type RunOptions struct {
	TaskID  string
	SubTask string
}

type RuntimeContext struct {
	TaskID         string
	SubTaskPath    string
	ToolConfigPath string
	InputPath      string
	OutputPath     string
	InputURL       string
	OutputURL      string
}

func New(stdout, stderr io.Writer, logger *slog.Logger) *Executor {
	return &Executor{
		stdout: stdout,
		stderr: stderr,
		logger: logging.Component(logger, "executor"),
	}
}

func (e *Executor) Describe(format string) error {
	e.logger.Info("describe requested", "format", format)
	toolSpec, err := loadToolSpec()
	if err != nil {
		e.logger.Error("describe failed to load tool spec", "error", err)
		return err
	}

	switch format {
	case "", "yaml":
		payload, err := yaml.Marshal(toolSpec)
		if err != nil {
			e.logger.Error("describe failed to marshal yaml", "error", err)
			return fmt.Errorf("marshal tool spec as yaml: %w", err)
		}
		_, err = e.stdout.Write(payload)
		if err == nil {
			e.logger.Debug("describe wrote yaml payload", "bytes", len(payload))
		}
		return err
	case "json":
		e.logger.Debug("describe wrote json payload")
		return json.NewEncoder(e.stdout).Encode(toolSpec)
	default:
		e.logger.Warn("describe received unsupported format", "format", format)
		return fmt.Errorf("unsupported describe format %q", format)
	}
}

func (e *Executor) Run(ctx context.Context, options RunOptions) error {
	runtimeContext, err := loadRuntimeContext(options)
	if err != nil {
		e.logger.Error("executor runtime context invalid", "error", err)
		return err
	}
	runLogger := e.logger.With("task_id", runtimeContext.TaskID, "output_path", runtimeContext.OutputPath)
	runLogger.Info("executor run started")

	if err := os.MkdirAll(runtimeContext.OutputPath, 0o755); err != nil {
		runLogger.Error("failed to create output directory", "error", err)
		return fmt.Errorf("create output directory: %w", err)
	}

	toolSpec, err := loadToolSpecFrom(runtimeContext.ToolConfigPath)
	if err != nil {
		runLogger.Debug("merged tool config unavailable, falling back to local tool spec", "tool_config", runtimeContext.ToolConfigPath, "error", err)
		toolSpec, err = loadToolSpec()
		if err != nil {
			runLogger.Error("failed to load tool spec", "error", err)
			return err
		}
	}
	runLogger = runLogger.With("tool", toolSpec.Name)

	hookPath := resolveRunHook()
	if hookPath != "" {
		runLogger.Info("executing tool hook", "hook_path", hookPath)
		if err := e.runHook(ctx, hookPath); err != nil {
			runLogger.Error("tool hook failed", "hook_path", hookPath, "error", err)
			_ = writeFailureHandoff(runtimeContext.OutputPath, toolSpec.Name, runtimeContext.TaskID, err)
			return err
		}

		handoffPath := filepath.Join(runtimeContext.OutputPath, "_handoff.md")
		if _, err := os.Stat(handoffPath); err == nil {
			runLogger.Info("tool hook completed with explicit handoff", "hook_path", hookPath)
			return nil
		}

		runLogger.Info("tool hook completed without handoff; generating fallback handoff", "hook_path", hookPath)
		return writeAutoHandoff(runtimeContext.OutputPath, toolSpec.Name, runtimeContext.TaskID, "hook completed without explicit handoff")
	}

	runLogger.Info("no tool hook found; writing scaffold artifacts")
	return e.writeScaffoldArtifacts(runtimeContext, toolSpec)
}

func (e *Executor) runHook(ctx context.Context, hookPath string) error {
	cmd := exec.CommandContext(ctx, hookPath)
	cmd.Stdout = e.stdout
	cmd.Stderr = e.stderr
	cmd.Env = os.Environ()
	cmd.Dir = filepath.Dir(hookPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run tool hook %q: %w", hookPath, err)
	}
	return nil
}

func (e *Executor) writeScaffoldArtifacts(runtimeContext RuntimeContext, toolSpec spec.ToolSpec) error {
	subTaskPayload, _ := os.ReadFile(runtimeContext.SubTaskPath)
	configPayload, _ := os.ReadFile(runtimeContext.ToolConfigPath)

	report := map[string]any{
		"task_id":          runtimeContext.TaskID,
		"tool":             toolSpec.Name,
		"description":      toolSpec.Description,
		"input_path":       runtimeContext.InputPath,
		"output_path":      runtimeContext.OutputPath,
		"sub_task_path":    runtimeContext.SubTaskPath,
		"tool_config_path": runtimeContext.ToolConfigPath,
		"input_url":        runtimeContext.InputURL,
		"output_url":       runtimeContext.OutputURL,
		"sub_task":         strings.TrimSpace(string(subTaskPayload)),
		"tool_config":      strings.TrimSpace(string(configPayload)),
		"generated_at":     time.Now().UTC().Format(time.RFC3339),
	}

	reportPath := filepath.Join(runtimeContext.OutputPath, "execution-report.json")
	reportPayload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal execution report: %w", err)
	}
	if err := os.WriteFile(reportPath, reportPayload, 0o644); err != nil {
		return fmt.Errorf("write execution report: %w", err)
	}

	handoffBody := "## Description\n\n" +
		"This output was produced by the built-in scaffold executor.\n\n" +
		"- A tool-specific run hook was not found.\n" +
		"- The executor wrote `execution-report.json` as a placeholder artifact.\n" +
		"- Replace it with `/opt/tool/run` or `DANGO_TOOL_RUN` to execute real tool logic.\n"

	return writeHandoff(runtimeContext.OutputPath, spec.Handoff{
		Metadata: spec.HandoffMetadata{
			TaskID:      runtimeContext.TaskID,
			Tool:        toolSpec.Name,
			Status:      spec.HandoffStatusCompleted,
			OutputFiles: []string{"execution-report.json"},
			Timestamp:   time.Now().UTC(),
		},
		Body: handoffBody,
	})
}

func loadRuntimeContext(options RunOptions) (RuntimeContext, error) {
	taskID := firstNonEmpty(options.TaskID, os.Getenv("TASK_ID"))
	if strings.TrimSpace(taskID) == "" {
		return RuntimeContext{}, fmt.Errorf("task id is required via --task-id or TASK_ID")
	}

	subTaskPath := firstNonEmpty(options.SubTask, os.Getenv("SUB_TASK"))
	if strings.TrimSpace(subTaskPath) == "" {
		return RuntimeContext{}, fmt.Errorf("sub-task path is required via --sub-task or SUB_TASK")
	}

	outputPath := strings.TrimSpace(os.Getenv("OUTPUT_PATH"))
	if outputPath == "" {
		return RuntimeContext{}, fmt.Errorf("OUTPUT_PATH is required")
	}

	return RuntimeContext{
		TaskID:         taskID,
		SubTaskPath:    subTaskPath,
		ToolConfigPath: strings.TrimSpace(os.Getenv("TOOL_CONFIG")),
		InputPath:      strings.TrimSpace(os.Getenv("INPUT_PATH")),
		OutputPath:     outputPath,
		InputURL:       strings.TrimSpace(os.Getenv("INPUT_URL")),
		OutputURL:      strings.TrimSpace(os.Getenv("OUTPUT_URL")),
	}, nil
}

func loadToolSpec() (spec.ToolSpec, error) {
	for _, candidate := range []string{
		strings.TrimSpace(os.Getenv("DANGO_TOOL_YAML")),
		"/opt/tool/tool.yaml",
		"tool.yaml",
	} {
		if candidate == "" {
			continue
		}
		toolSpec, err := loadToolSpecFrom(candidate)
		if err == nil {
			return toolSpec, nil
		}
	}

	return spec.ToolSpec{}, fmt.Errorf("tool.yaml was not found via DANGO_TOOL_YAML, /opt/tool/tool.yaml, or ./tool.yaml")
}

func loadToolSpecFrom(path string) (spec.ToolSpec, error) {
	if strings.TrimSpace(path) == "" {
		return spec.ToolSpec{}, fmt.Errorf("tool spec path is empty")
	}

	payload, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return spec.ToolSpec{}, fmt.Errorf("read tool spec %q: %w", path, err)
	}

	var toolSpec spec.ToolSpec
	if err := yaml.Unmarshal(payload, &toolSpec); err != nil {
		return spec.ToolSpec{}, fmt.Errorf("parse tool spec %q: %w", path, err)
	}

	if err := toolSpec.Validate(); err != nil {
		return spec.ToolSpec{}, err
	}

	return toolSpec, nil
}

func resolveRunHook() string {
	for _, candidate := range []string{
		strings.TrimSpace(os.Getenv("DANGO_TOOL_RUN")),
		"/opt/tool/run",
		"/opt/tool/bin/run",
	} {
		if candidate == "" {
			continue
		}

		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}

		if info.Mode()&0o111 != 0 {
			return candidate
		}
	}

	return ""
}

func writeAutoHandoff(outputPath, toolName, taskID, summary string) error {
	files, err := collectOutputFiles(outputPath)
	if err != nil {
		return err
	}

	return writeHandoff(outputPath, spec.Handoff{
		Metadata: spec.HandoffMetadata{
			TaskID:      taskID,
			Tool:        toolName,
			Status:      spec.HandoffStatusCompleted,
			OutputFiles: files,
			Timestamp:   time.Now().UTC(),
		},
		Body: "## Description\n\n" + summary,
	})
}

func writeFailureHandoff(outputPath, toolName, taskID string, executionErr error) error {
	return writeHandoff(outputPath, spec.Handoff{
		Metadata: spec.HandoffMetadata{
			TaskID:    taskID,
			Tool:      toolName,
			Status:    spec.HandoffStatusFailed,
			Timestamp: time.Now().UTC(),
			Error:     executionErr.Error(),
		},
		Body: "## Description\n\nTool execution failed before producing a handoff.",
	})
}

func writeHandoff(outputPath string, handoff spec.Handoff) error {
	payload, err := spec.RenderHandoff(handoff)
	if err != nil {
		return err
	}

	path := filepath.Join(outputPath, "_handoff.md")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write handoff %q: %w", path, err)
	}
	return nil
}

func collectOutputFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "_handoff.md" {
			return nil
		}

		out = append(out, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk output directory %q: %w", root, err)
	}

	sort.Strings(out)
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
