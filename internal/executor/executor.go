package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/spec"
	"gopkg.in/yaml.v3"
)

// Executor serves the runtime entrypoints used inside tool containers.
//
// An Executor is safe to reuse across multiple describe and run calls as long
// as its output writers remain valid for the lifetime of the calls.
type Executor struct {
	stdout io.Writer
	stderr io.Writer
	logger *slog.Logger
}

// RunOptions describes the CLI-provided inputs for executor runs.
type RunOptions struct {
	// TaskID identifies the task being executed. If empty, TASK_ID is used.
	TaskID string
	// SubTask points to the sub-task markdown file. If empty, SUB_TASK is used.
	SubTask string
}

// New constructs an [Executor] that writes command results to stdout and
// diagnostics to stderr.
//
// When logger is nil, logging falls back to a discard logger.
func New(stdout, stderr io.Writer, logger *slog.Logger) *Executor {
	return &Executor{
		stdout: stdout,
		stderr: stderr,
		logger: logging.Component(logger, "executor"),
	}
}

// Describe emits the local tool specification in format.
//
// Supported formats are "yaml" (default) and "json".
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

// Run executes a tool task using the scheduler environment contract.
//
// Run validates runtime inputs, ensures OUTPUT_PATH exists, executes a tool
// hook when available, and guarantees that a _handoff.md is written even for
// scaffold fallback behavior.
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

func (e *Executor) writeScaffoldArtifacts(runtimeContext runtimeContext, toolSpec spec.ToolSpec) error {
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
