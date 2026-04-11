package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/spec"
)

type staticLLMClient struct {
	mu        sync.Mutex
	responses []staticLLMResponse
	index     int
}

type staticLLMResponse struct {
	payload []byte
	err     error
}

func (c *staticLLMClient) CompleteJSON(_ context.Context, _ llm.Request) ([]byte, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.index >= len(c.responses) {
		// Fall back to the last response when exhausted so single-response
		// tests continue to work without modification.
		last := c.responses[len(c.responses)-1]
		if last.err != nil {
			return nil, "", last.err
		}
		return append([]byte(nil), last.payload...), "", nil
	}
	r := c.responses[c.index]
	c.index++
	if r.err != nil {
		return nil, "", r.err
	}
	return append([]byte(nil), r.payload...), fmt.Sprintf("resp_%d", c.index), nil
}

func staticLLMFactory(payload []byte, err error) llmClientFactory {
	return func(string, *slog.Logger) llm.Client {
		return &staticLLMClient{responses: []staticLLMResponse{{payload: payload, err: err}}}
	}
}

func sequentialLLMFactory(resps ...staticLLMResponse) llmClientFactory {
	client := &staticLLMClient{responses: resps}
	return func(string, *slog.Logger) llm.Client {
		return client
	}
}

func TestExecutorRunWithoutHookUsesBuiltInAI(t *testing.T) {
	root := t.TempDir()
	toolPath := filepath.Join(root, "tool.yaml")
	subTaskPath := filepath.Join(root, "sub-task.md")
	inputPath := filepath.Join(root, "input")
	outputPath := filepath.Join(root, "output")
	privateOutputPath := filepath.Join(root, "_output")

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
	if err := os.WriteFile(subTaskPath, []byte("Write a final report based on the input brief."), 0o644); err != nil {
		t.Fatalf("write sub-task.md: %v", err)
	}
	if err := os.MkdirAll(inputPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(input): %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputPath, "brief.md"), []byte("Customer wants a concise launch update."), 0o644); err != nil {
		t.Fatalf("write brief.md: %v", err)
	}

	t.Setenv("DANGO_TOOL_YAML", toolPath)
	t.Setenv("TASK_ID", "task-123")
	t.Setenv("SUB_TASK", subTaskPath)
	t.Setenv("TOOL_CONFIG", toolPath)
	t.Setenv("INPUT_PATH", inputPath)
	t.Setenv("OUTPUT_PATH", outputPath)
	t.Setenv("PRIVATE_OUTPUT_PATH", privateOutputPath)

	execMode := newWithLLMFactory(os.Stdout, os.Stderr, nil, sequentialLLMFactory(
		// Turn 1: detail-planning response
		staticLLMResponse{payload: []byte(`{
			"summary":"Generated the final report.",
			"sub_task":"Read the input brief and produce a PDF report.",
			"expected_outputs":["final-report.md"]
		}`)},
		// Turn 2: execute-generation response (continues conversation)
		staticLLMResponse{payload: []byte(`{
			"summary":"Generated the final report.",
			"handoff_body":"## Description\n\nGenerated the final report from the provided input brief.",
			"expected_outputs":["final-report.md"],
			"generated_artifacts":[
				{"path":"final-report.md","description":"Final report","content":"# Final Report\n\nLaunch is on track."},
				{"path":"notes/internal-summary.md","private":true,"description":"Private notes","content":"Internal note."}
			]
		}`)},
	))
	if err := execMode.Run(context.Background(), RunOptions{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	publicPayload, err := os.ReadFile(filepath.Join(outputPath, "final-report.md"))
	if err != nil {
		t.Fatalf("read public artifact: %v", err)
	}
	if !strings.Contains(string(publicPayload), "Launch is on track") {
		t.Fatalf("public artifact = %q, want generated content", string(publicPayload))
	}
	if _, err := os.Stat(filepath.Join(privateOutputPath, "final-report.md")); err != nil {
		t.Fatalf("private mirrored artifact stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputPath, "notes", "internal-summary.md")); !os.IsNotExist(err) {
		t.Fatalf("private-only artifact should not exist in public output, stat error = %v", err)
	}

	handoffPayload, err := os.ReadFile(filepath.Join(privateOutputPath, "_handoff.md"))
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
	if got, want := strings.Join(handoff.Metadata.OutputFiles, ","), "final-report.md"; got != want {
		t.Fatalf("handoff.Metadata.OutputFiles = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(outputPath, "handoff.md")); err != nil {
		t.Fatalf("handoff.md missing: %v", err)
	}
}

func TestExecutorPlanWithoutHookUsesBuiltInAI(t *testing.T) {
	root := t.TempDir()
	toolPath := filepath.Join(root, "tool.yaml")
	subTaskPath := filepath.Join(root, "sub-task.md")

	toolYAML := []byte("" +
		"name: plannerless-tool\n" +
		"version: 1.0.0\n" +
		"description: Has no plan hook\n" +
		"input_types: [request]\n" +
		"output_types: [final]\n" +
		"model: local/plannerless-tool\n")
	if err := os.WriteFile(toolPath, toolYAML, 0o644); err != nil {
		t.Fatalf("write tool.yaml: %v", err)
	}
	if err := os.WriteFile(subTaskPath, []byte("# sub-task"), 0o644); err != nil {
		t.Fatalf("write sub-task.md: %v", err)
	}

	t.Setenv("DANGO_TOOL_YAML", toolPath)
	t.Setenv("TASK_ID", "task-456")
	t.Setenv("SUB_TASK", subTaskPath)
	t.Setenv("TOOL_CONFIG", toolPath)

	var stdout bytes.Buffer
	err := newWithLLMFactory(&stdout, os.Stderr, nil, staticLLMFactory([]byte(`{
		"summary":"Produce the final answer.",
		"sub_task":"Read the request and produce the final artifact.",
		"expected_outputs":["final-answer.md"]
	}`), nil)).Plan(context.Background(), PlanOptions{Format: "json"})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	var plan spec.ExecutorPlan
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &plan); err != nil {
		t.Fatalf("json.Unmarshal(plan) error = %v", err)
	}
	if got, want := plan.Summary, "Produce the final answer."; got != want {
		t.Fatalf("plan.Summary = %q, want %q", got, want)
	}
	if got, want := strings.Join(plan.ExpectedOutputs, ","), "final-answer.md"; got != want {
		t.Fatalf("plan.ExpectedOutputs = %q, want %q", got, want)
	}
}

func TestExecutorRunWithoutHookFailsWhenBuiltInAIUnavailable(t *testing.T) {
	root := t.TempDir()
	toolPath := filepath.Join(root, "tool.yaml")
	subTaskPath := filepath.Join(root, "sub-task.md")
	outputPath := filepath.Join(root, "output")
	privateOutputPath := filepath.Join(root, "_output")

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
	t.Setenv("TASK_ID", "task-789")
	t.Setenv("SUB_TASK", subTaskPath)
	t.Setenv("TOOL_CONFIG", toolPath)
	t.Setenv("OUTPUT_PATH", outputPath)
	t.Setenv("PRIVATE_OUTPUT_PATH", privateOutputPath)

	err := newWithLLMFactory(os.Stdout, os.Stderr, nil, staticLLMFactory(nil, errors.New("LLM unavailable"))).Run(context.Background(), RunOptions{})
	if err == nil {
		t.Fatal("Run() error = nil, want cannot-proceed error")
	}
	if !strings.Contains(err.Error(), "cannot proceed") {
		t.Fatalf("Run() error = %v, want cannot proceed message", err)
	}
}
