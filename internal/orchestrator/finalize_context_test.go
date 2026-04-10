package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/runner"
	"github.com/tsumina/dango/internal/runner/runtime"
	"github.com/tsumina/dango/internal/store/sqlite"
	"github.com/tsumina/dango/internal/taskflow"
	_ "modernc.org/sqlite"
)

type cancelingRuntime struct {
	describeYAML []byte
	cancel       context.CancelFunc
}

func (r *cancelingRuntime) Pull(_ context.Context, _ string) error {
	return nil
}

func (r *cancelingRuntime) DescribeTool(_ context.Context, _ string) ([]byte, error) {
	return r.describeYAML, nil
}

func (r *cancelingRuntime) PlanExecutor(_ context.Context, _ runtime.ExecutorPlanRequest) ([]byte, error) {
	return staticExecutorPlanJSON("Cancel run", "Run the canceling tool for this request.", []string{"result.final"}), nil
}

func (r *cancelingRuntime) RunExecutor(ctx context.Context, _ runtime.ExecutorRunRequest) error {
	if r.cancel != nil {
		r.cancel()
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestTaskRunnerRunPersistsCancellationAfterCancellation(t *testing.T) {
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

	rt := &cancelingRuntime{
		describeYAML: []byte("" +
			"name: canceling-tool\n" +
			"version: 1.0.0\n" +
			"description: Cancels during execution\n" +
			"input_types: [request]\n" +
			"output_types: [final]\n" +
			"model: demo/canceling-tool\n"),
	}
	registry := NewRegistryService(locator, store, rt, nil)
	if _, err := registry.Register(context.Background(), "example/canceling-tool:v1", ""); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	taskService := NewTaskService(locator, store, nil)
	planner := runner.NewPlannerWithClient(locator, store, rt, staticPlannerClient(t,
		staticPlannerDraftJSON(
			[]plannerDraftResponseEdge{
				{Ref: "cancel", ToolName: "canceling-tool", Dependencies: nil, InputType: "request", OutputType: "final", Title: "Cancel run", Summary: "Invoke the canceling tool once.", ExpectedOutputs: []string{"result.final"}, SubTask: "Run the canceling tool for this request."},
			},
		),
		plannerApprovedJSON(),
	), nil)
	scheduler := runner.NewScheduler(locator, store, rt, nil)
	runners := runner.NewTaskRunnerService(locator, taskService, planner, scheduler, nil)

	ctx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel

	result, err := runners.RunNow(ctx, taskflow.RequestEnvelope{Text: "trigger cancellation handling"})
	if err == nil {
		t.Fatal("Run() error = nil, want cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if result != nil {
		t.Fatalf("Run() result = %#v, want nil on failure", result)
	}

	db, err := sql.Open("sqlite", locator.DBPath())
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var taskID string
	var taskStatus string
	if err := db.QueryRow(`SELECT id, status FROM tasks LIMIT 1`).Scan(&taskID, &taskStatus); err != nil {
		t.Fatalf("query task status error = %v", err)
	}
	if got, want := taskStatus, "canceled"; got != want {
		t.Fatalf("task status = %q, want %q", got, want)
	}

	var edgeStatus string
	if err := db.QueryRow(`SELECT status FROM edges LIMIT 1`).Scan(&edgeStatus); err != nil {
		t.Fatalf("query edge status error = %v", err)
	}
	if got, want := edgeStatus, "failed"; got != want {
		t.Fatalf("edge status = %q, want %q", got, want)
	}

	if _, err := os.Stat(locator.TaskResultPath(taskID)); err != nil {
		t.Fatalf("task result stat error = %v", err)
	}
}
