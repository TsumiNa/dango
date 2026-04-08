package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tsumina/dango/internal/spec"
)

func TestExecutorRunScaffold(t *testing.T) {
	root := t.TempDir()
	toolPath := filepath.Join(root, "tool.yaml")
	subTaskPath := filepath.Join(root, "sub-task.md")
	outputPath := filepath.Join(root, "output")

	toolYAML := []byte("" +
		"name: pdf-generator\n" +
		"version: 1.0.0\n" +
		"description: Generates PDF reports\n" +
		"input_types: [json]\n" +
		"output_types: [pdf]\n" +
		"model: local/pdf-specialist-v2\n")
	if err := os.WriteFile(toolPath, toolYAML, 0o644); err != nil {
		t.Fatalf("write tool.yaml: %v", err)
	}
	if err := os.WriteFile(subTaskPath, []byte("# sub-task"), 0o644); err != nil {
		t.Fatalf("write sub-task.md: %v", err)
	}

	t.Setenv("DANGO_TOOL_YAML", toolPath)
	t.Setenv("TASK_ID", "task-123")
	t.Setenv("SUB_TASK", subTaskPath)
	t.Setenv("TOOL_CONFIG", toolPath)
	t.Setenv("OUTPUT_PATH", outputPath)

	execMode := New(os.Stdout, os.Stderr, nil)
	if err := execMode.Run(context.Background(), RunOptions{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(outputPath, "execution-report.json")); err != nil {
		t.Fatalf("execution-report.json missing: %v", err)
	}

	handoffPayload, err := os.ReadFile(filepath.Join(outputPath, "_handoff.md"))
	if err != nil {
		t.Fatalf("read _handoff.md: %v", err)
	}

	handoff, err := spec.ParseHandoff(handoffPayload)
	if err != nil {
		t.Fatalf("ParseHandoff() error = %v", err)
	}

	if got, want := handoff.Metadata.Tool, "pdf-generator"; got != want {
		t.Fatalf("handoff.Metadata.Tool = %q, want %q", got, want)
	}
	if got, want := handoff.Metadata.Status, spec.HandoffStatusCompleted; got != want {
		t.Fatalf("handoff.Metadata.Status = %q, want %q", got, want)
	}
}
