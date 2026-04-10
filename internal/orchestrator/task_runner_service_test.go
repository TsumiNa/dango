package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/runner"
	"github.com/tsumina/dango/internal/runner/runtime"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
	"github.com/tsumina/dango/internal/taskflow"
)

type blockingRuntime struct {
	describeYAML []byte
	started      chan struct{}
	release      chan struct{}
}

func (r *blockingRuntime) Pull(context.Context, string) error {
	return nil
}

func (r *blockingRuntime) DescribeTool(context.Context, string) ([]byte, error) {
	return r.describeYAML, nil
}

func (r *blockingRuntime) PlanExecutor(context.Context, runtime.ExecutorPlanRequest) ([]byte, error) {
	return staticExecutorPlanJSON("Background finalization", "Run the blocking tool and wait for completion.", []string{"result.final"}), nil
}

func (r *blockingRuntime) RunExecutor(ctx context.Context, request runtime.ExecutorRunRequest) error {
	select {
	case r.started <- struct{}{}:
	default:
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.release:
	}

	if err := os.MkdirAll(request.PublicOutputHost, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(request.PrivateOutputHost, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(request.PublicOutputHost, "result.final"), []byte("done"), 0o644); err != nil {
		return err
	}
	handoffPayload, err := spec.RenderHandoff(spec.Handoff{
		Metadata: spec.HandoffMetadata{
			TaskID:      request.TaskID,
			Tool:        "blocking-tool",
			Status:      spec.HandoffStatusCompleted,
			OutputFiles: []string{"result.final"},
			Timestamp:   time.Now().UTC(),
		},
		Body: "## Description\n\nCompleted in the background.",
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(request.PrivateOutputHost, "_handoff.md"), handoffPayload, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(request.PublicOutputHost, "handoff.md"), handoffPayload, 0o644); err != nil {
		return err
	}
	return nil
}

func TestTaskRunnerServiceStartRunsInBackground(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
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

	rt := &blockingRuntime{
		describeYAML: []byte("" +
			"name: blocking-tool\n" +
			"version: 1.0.0\n" +
			"description: Completes after an explicit release\n" +
			"input_types: [request]\n" +
			"output_types: [final]\n" +
			"model: local/blocking-tool\n"),
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}

	registry := NewRegistryService(locator, store, rt, nil)
	if _, err := registry.Register(context.Background(), "example/blocking-tool:v1", ""); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	taskService := NewTaskService(locator, store, nil)
	planner := runner.NewPlannerWithClient(locator, store, rt, staticPlannerClient(t,
		staticPlannerDraftJSON(
			[]plannerDraftResponseEdge{{
				Ref:             "final",
				ToolName:        "blocking-tool",
				Dependencies:    nil,
				InputType:       "request",
				OutputType:      "final",
				Title:           "Background finalization",
				Summary:         "Run the blocking tool in the background.",
				ExpectedOutputs: []string{"result.final"},
				SubTask:         "Run the blocking tool and wait for completion.",
			}},
		),
		plannerApprovedJSON(),
	), nil)
	scheduler := runner.NewScheduler(locator, store, rt, nil)
	runners := runner.NewTaskRunnerService(locator, taskService, planner, scheduler, nil)

	returned := make(chan struct{})
	var description *taskflow.TaskDescription
	var startErr error
	go func() {
		description, startErr = runners.Start(context.Background(), taskflow.RequestEnvelope{Text: "run in background"})
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Start() did not return before background execution completed")
	}
	if startErr != nil {
		t.Fatalf("Start() error = %v", startErr)
	}
	if description == nil {
		t.Fatal("Start() description = nil")
	}

	select {
	case <-rt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background executor did not start")
	}

	current, err := runners.Describe(context.Background(), description.Task.ID)
	if err != nil {
		t.Fatalf("Describe() error = %v", err)
	}
	if current.Task.Status == string(spec.TaskStatusDone) {
		t.Fatalf("task status = %q, want unfinished status before release", current.Task.Status)
	}

	close(rt.release)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, err = runners.Describe(context.Background(), description.Task.ID)
		if err != nil {
			t.Fatalf("Describe() error = %v", err)
		}
		if current.Task.Status == string(spec.TaskStatusDone) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}

	payload, _ := json.Marshal(current)
	t.Fatalf("task did not reach done status after release: %s", payload)
}
