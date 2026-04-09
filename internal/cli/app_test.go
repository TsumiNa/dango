package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestAppRunWithoutArgsShowsHelp(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	app := New(&stdout, &stderr)
	if err := app.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "orchestrator") {
		t.Fatalf("help output = %q, want orchestrator command", output)
	}
	if !strings.Contains(output, "executor") {
		t.Fatalf("help output = %q, want executor command", output)
	}
}

func TestAppRunExecutorDescribeJSON(t *testing.T) {
	toolPath := writeToolSpec(t)
	t.Setenv("DANGO_TOOL_YAML", toolPath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	app := New(&stdout, &stderr)
	err := app.Run(context.Background(), []string{"executor", "describe", "--format", "json", "--log-level", "error"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal(stdout) error = %v; output = %q", err, stdout.String())
	}
	if got, want := payload["name"], "pdf-generator"; got != want {
		t.Fatalf("payload[name] = %v, want %q", got, want)
	}
}

func TestAppRunUnknownCommandReturnsError(t *testing.T) {
	t.Parallel()

	app := New(&bytes.Buffer{}, &bytes.Buffer{})
	err := app.Run(context.Background(), []string{"wat"})
	if err == nil {
		t.Fatal("Run() error = nil, want error")
	}
	if !strings.Contains(err.Error(), `unknown command "wat"`) {
		t.Fatalf("Run() error = %v, want unknown command", err)
	}
}

func writeToolSpec(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	toolPath := root + "/tool.yaml"
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

	return toolPath
}
