package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/runner"
	"github.com/tsumina/dango/internal/runner/runtime"
	"github.com/tsumina/dango/internal/store/sqlite"
	"github.com/tsumina/dango/internal/taskflow"
)

func TestTaskRunnerRunEndToEndWithHostRuntime(t *testing.T) {
	root := t.TempDir()
	toolsRoot := filepath.Join(root, "tools")
	for _, tool := range []struct {
		Name        string
		InputType   string
		OutputType  string
		NextToolYML string
		OutputFile  string
	}{
		{Name: "toy-brief", InputType: "request", OutputType: "brief", NextToolYML: `"toy-drafter"`, OutputFile: "brief.md"},
		{Name: "toy-drafter", InputType: "brief", OutputType: "draft", NextToolYML: `"toy-packager"`, OutputFile: "draft.md"},
		{Name: "toy-packager", InputType: "draft", OutputType: "final", NextToolYML: "null", OutputFile: "final-report.md"},
	} {
		createTempTool(t, toolsRoot, tool.Name, tool.InputType, tool.OutputType, tool.NextToolYML, tool.OutputFile)
	}

	locator, err := datadir.New(filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("datadir.New() error = %v", err)
	}
	if err := locator.Ensure(); err != nil {
		t.Fatalf("locator.Ensure() error = %v", err)
	}

	store, err := sqlite.Open(locator.DBPath())
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rt := runtime.NewDefault("", nil)
	registry := NewRegistryService(locator, store, rt, nil)
	for _, name := range []string{"toy-brief", "toy-drafter", "toy-packager"} {
		if _, err := registry.Register(context.Background(), runtime.HostPrefix+filepath.Join(toolsRoot, name), ""); err != nil {
			t.Fatalf("Register(%s) error = %v", name, err)
		}
	}

	taskService := NewTaskService(locator, store, nil)
	planner := runner.NewPlannerWithClient(locator, store, rt, staticPlannerClient(t,
		staticPlannerDraftJSON(
			[]plannerDraftResponseEdge{
				{Ref: "brief", ToolName: "toy-brief", Dependencies: nil, InputType: "request", OutputType: "brief", Title: "Create brief", Summary: "Produce a brief from the request.", ExpectedOutputs: []string{"brief.md"}, SubTask: "Read the request and produce a short brief artifact."},
				{Ref: "draft", ToolName: "toy-drafter", Dependencies: []string{"brief"}, InputType: "brief", OutputType: "draft", Title: "Draft content", Summary: "Expand the brief into a draft.", ExpectedOutputs: []string{"draft.md"}, SubTask: "Use the brief input and produce a draft output."},
				{Ref: "final", ToolName: "toy-packager", Dependencies: []string{"draft"}, InputType: "draft", OutputType: "final", Title: "Package result", Summary: "Turn the draft into the final artifact.", ExpectedOutputs: []string{"final-report.md"}, SubTask: "Package the draft into final deliverables."},
			},
		),
		plannerApprovedJSON(),
	), nil)
	scheduler := runner.NewScheduler(locator, store, rt, nil)
	runners := runner.NewTaskRunnerService(locator, taskService, planner, scheduler, nil)

	result, err := runners.RunNow(context.Background(), taskflow.RequestEnvelope{Text: "write a small demo artifact"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := result.Task.Status, "done"; got != want {
		t.Fatalf("result.Task.Status = %q, want %q", got, want)
	}
	if got, want := len(result.Plan.Edges), 3; got != want {
		t.Fatalf("len(result.Plan.Edges) = %d, want %d", got, want)
	}
	if got, want := len(result.TerminalHandoffs), 1; got != want {
		t.Fatalf("len(result.TerminalHandoffs) = %d, want %d", got, want)
	}

	if _, err := os.Stat(result.ResultPath); err != nil {
		t.Fatalf("result.ResultPath stat error = %v", err)
	}

	finalEdgeID := result.Plan.Edges[len(result.Plan.Edges)-1].ID
	finalArtifact := filepath.Join(locator.EdgeOutputDir(result.Task.ID, finalEdgeID), "final-report.md")
	if _, err := os.Stat(finalArtifact); err != nil {
		t.Fatalf("final artifact stat error = %v", err)
	}
}

func TestTaskRunnerRunEndToEndWithHostRuntimeWithoutToolHooks(t *testing.T) {
	root := t.TempDir()
	toolsRoot := filepath.Join(root, "tools")
	createTempToolWithoutHooks(t, toolsRoot, "toy-no-hooks", "request", "final")
	executorBin := createFakeHostExecutorBinary(t, root)
	t.Setenv("DANGO_HOST_EXECUTOR_BIN", executorBin)

	locator, err := datadir.New(filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("datadir.New() error = %v", err)
	}
	if err := locator.Ensure(); err != nil {
		t.Fatalf("locator.Ensure() error = %v", err)
	}

	store, err := sqlite.Open(locator.DBPath())
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rt := runtime.NewDefault("", nil)
	registry := NewRegistryService(locator, store, rt, nil)
	if _, err := registry.Register(context.Background(), runtime.HostPrefix+filepath.Join(toolsRoot, "toy-no-hooks"), ""); err != nil {
		t.Fatalf("Register(toy-no-hooks) error = %v", err)
	}

	taskService := NewTaskService(locator, store, nil)
	planner := runner.NewPlannerWithClient(locator, store, rt, staticPlannerClient(t,
		staticPlannerDraftJSON([]plannerDraftResponseEdge{{
			Ref:             "final",
			ToolName:        "toy-no-hooks",
			Dependencies:    nil,
			InputType:       "request",
			OutputType:      "final",
			Title:           "Finalize task",
			Summary:         "Produce the final output.",
			ExpectedOutputs: []string{"result.final"},
			SubTask:         "Produce the final output using the executor fallback.",
		}}),
		plannerApprovedJSON(),
	), nil)
	scheduler := runner.NewScheduler(locator, store, rt, nil)
	runners := runner.NewTaskRunnerService(locator, taskService, planner, scheduler, nil)

	result, err := runners.RunNow(context.Background(), taskflow.RequestEnvelope{Text: "run the no-hook tool"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := result.Task.Status, "done"; got != want {
		t.Fatalf("result.Task.Status = %q, want %q", got, want)
	}
	if got, want := len(result.Plan.Edges), 1; got != want {
		t.Fatalf("len(result.Plan.Edges) = %d, want %d", got, want)
	}

	finalEdgeID := result.Plan.Edges[0].ID
	finalArtifact := filepath.Join(locator.EdgeOutputDir(result.Task.ID, finalEdgeID), "result.final")
	if _, err := os.Stat(finalArtifact); err != nil {
		t.Fatalf("final artifact stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(locator.EdgePrivateOutputDir(result.Task.ID, finalEdgeID), "_handoff.md")); err != nil {
		t.Fatalf("private handoff stat error = %v", err)
	}
}

func createTempTool(t *testing.T, root, name, inputType, outputType, nextToolYAML, outputFile string) {
	t.Helper()

	toolDir := filepath.Join(root, name)
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", toolDir, err)
	}

	toolYAML := fmt.Sprintf(`name: "%s"
version: "0.1.0"
description: "temp test tool %s"
input_types: ["%s"]
output_types: ["%s"]
model: "demo/%s"
`, name, name, inputType, outputType, name)
	if err := os.WriteFile(filepath.Join(toolDir, "tool.yaml"), []byte(toolYAML), 0o644); err != nil {
		t.Fatalf("write tool.yaml error = %v", err)
	}

	planScript := fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s' '{"summary":"Refined execution plan for %s.","sub_task":"Execute the assigned stage using %s.","expected_outputs":["%s"]}'
`, name, name, outputFile)
	planPath := filepath.Join(toolDir, "plan")
	if err := os.WriteFile(planPath, []byte(planScript), 0o755); err != nil {
		t.Fatalf("write plan script error = %v", err)
	}
	if err := os.Chmod(planPath, 0o755); err != nil {
		t.Fatalf("chmod plan script error = %v", err)
	}

	runScript := fmt.Sprintf(`#!/bin/sh
set -eu
timestamp="$(date -u +"%%Y-%%m-%%dT%%H:%%M:%%SZ")"
mkdir -p "${OUTPUT_PATH}" "${PRIVATE_OUTPUT_PATH}"
cat > "${OUTPUT_PATH}/%s" <<EOF
generated by %s
EOF
cat > "${PRIVATE_OUTPUT_PATH}/%s" <<EOF
generated by %s
EOF
cat > "${PRIVATE_OUTPUT_PATH}/_handoff.md" <<EOF
---
task_id: "${TASK_ID}"
tool: "%s"
status: "completed"
output_files:
  - %s
next_tool: %s
timestamp: "${timestamp}"
---

## Description

Generated %s.
EOF
cp "${PRIVATE_OUTPUT_PATH}/_handoff.md" "${OUTPUT_PATH}/handoff.md"
`, outputFile, name, outputFile, name, name, outputFile, nextToolYAML, outputFile)
	runPath := filepath.Join(toolDir, "run")
	if err := os.WriteFile(runPath, []byte(runScript), 0o755); err != nil {
		t.Fatalf("write run script error = %v", err)
	}
	if err := os.Chmod(runPath, 0o755); err != nil {
		t.Fatalf("chmod run script error = %v", err)
	}
}

func createTempToolWithoutHooks(t *testing.T, root, name, inputType, outputType string) {
	t.Helper()

	toolDir := filepath.Join(root, name)
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", toolDir, err)
	}

	toolYAML := fmt.Sprintf(`name: "%s"
version: "0.1.0"
description: "temp test tool %s without explicit hooks"
input_types: ["%s"]
output_types: ["%s"]
model: "demo/%s"
`, name, name, inputType, outputType, name)
	if err := os.WriteFile(filepath.Join(toolDir, "tool.yaml"), []byte(toolYAML), 0o644); err != nil {
		t.Fatalf("write tool.yaml error = %v", err)
	}
}

func createFakeHostExecutorBinary(t *testing.T, root string) string {
	t.Helper()

	path := filepath.Join(root, "fake-host-executor.sh")
	script := `#!/bin/sh
set -eu

case "${1:-}:${2:-}" in
executor:plan)
  printf '%s' '{"summary":"Fallback executor plan","sub_task":"Execute the assigned stage without tool hooks.","expected_outputs":["result.final"]}'
  ;;
executor:run)
  mkdir -p "${OUTPUT_PATH}" "${PRIVATE_OUTPUT_PATH}"
  cat > "${OUTPUT_PATH}/result.final" <<'EOF'
generated by fallback executor
EOF
  cat > "${PRIVATE_OUTPUT_PATH}/result.final" <<'EOF'
generated by fallback executor
EOF
  tool_name="$(awk -F': *' '$1=="name" {gsub(/"/, "", $2); print $2; exit}' "${DANGO_TOOL_YAML}")"
  if [ -z "${tool_name}" ]; then
    tool_name="fallback-tool"
  fi
  cat > "${PRIVATE_OUTPUT_PATH}/_handoff.md" <<EOF
---
task_id: "${TASK_ID}"
tool: "${tool_name}"
status: "completed"
output_files:
  - result.final
timestamp: "2025-01-01T00:00:00Z"
---

## Description

Generated result.final without plan/run hooks.
EOF
  cp "${PRIVATE_OUTPUT_PATH}/_handoff.md" "${OUTPUT_PATH}/handoff.md"
  ;;
*)
  echo "unexpected args: $*" >&2
  exit 1
  ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake executor binary error = %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod fake executor binary error = %v", err)
	}
	return path
}
