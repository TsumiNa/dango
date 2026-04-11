package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/tsumina/dango/internal/ai"
	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/runner"
	"github.com/tsumina/dango/internal/runner/runtime"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
	"github.com/tsumina/dango/internal/taskflow"
)

type plannerDraftResponse struct {
	Mode  string                     `json:"mode"`
	Edges []plannerDraftResponseEdge `json:"edges"`
}

type plannerDraftResponseEdge struct {
	Ref             string   `json:"ref"`
	ToolName        string   `json:"tool_name"`
	Dependencies    []string `json:"dependencies"`
	InputType       string   `json:"input_type"`
	OutputType      string   `json:"output_type"`
	Title           string   `json:"title"`
	Summary         string   `json:"summary"`
	ExpectedOutputs []string `json:"expected_outputs"`
	SubTask         string   `json:"sub_task"`
}

func TestPlannerPlanUsesLLMOutput(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	locator, err := datadir.New(root)
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

	seedTool := func(tool spec.ToolSpec) {
		payload, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		if err := store.UpsertTool(context.Background(), sqlite.ToolRecord{
			Name:       tool.Name,
			Image:      "host:///tmp/" + tool.Name,
			ConfigJSON: string(payload),
		}); err != nil {
			t.Fatalf("UpsertTool() error = %v", err)
		}
	}

	seedTool(spec.ToolSpec{
		Name:        "toy-brief",
		Version:     "0.1.0",
		Description: "brief stage",
		InputTypes:  []string{"request"},
		OutputTypes: []string{"brief"},
		Model:       "demo/toy-brief",
	})
	seedTool(spec.ToolSpec{
		Name:        "toy-drafter",
		Version:     "0.1.0",
		Description: "draft stage",
		InputTypes:  []string{"brief"},
		OutputTypes: []string{"draft"},
		Model:       "demo/toy-drafter",
	})
	seedTool(spec.ToolSpec{
		Name:        "toy-packager",
		Version:     "0.1.0",
		Description: "final stage",
		InputTypes:  []string{"draft"},
		OutputTypes: []string{"final"},
		Model:       "demo/toy-packager",
	})

	planner := runner.NewPlanner(locator, store, staticExecutorPlanRuntime{payload: staticExecutorPlanJSON("Executor detail plan", "Refine and execute the assigned stage.", []string{"detail-output.txt"})}, staticPlannerClient(t,
		staticPlannerDraftJSON(
			[]plannerDraftResponseEdge{
				{Ref: "brief", ToolName: "toy-brief", Dependencies: nil, InputType: "request", OutputType: "brief", Title: "Create brief", Summary: "Produce a brief from the request.", ExpectedOutputs: []string{"brief.md"}, SubTask: "Read the request and produce a short brief artifact."},
				{Ref: "draft", ToolName: "toy-drafter", Dependencies: []string{"brief"}, InputType: "brief", OutputType: "draft", Title: "Draft content", Summary: "Expand the brief into a draft.", ExpectedOutputs: []string{"draft.md"}, SubTask: "Use the brief input and produce a draft output."},
				{Ref: "final", ToolName: "toy-packager", Dependencies: []string{"draft"}, InputType: "draft", OutputType: "final", Title: "Package result", Summary: "Turn the draft into the final artifact.", ExpectedOutputs: []string{"final-report.md"}, SubTask: "Package the draft into final deliverables."},
			},
		),
		plannerApprovedJSON(),
	), nil)
	plan, err := planner.Plan(context.Background(), "task-123", taskflow.RequestEnvelope{Text: "write a small project status update"})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	if got, want := len(plan.Edges), 3; got != want {
		t.Fatalf("len(plan.Edges) = %d, want %d", got, want)
	}
	if got, want := plan.Edges[0].ToolName, "toy-brief"; got != want {
		t.Fatalf("plan.Edges[0].ToolName = %q, want %q", got, want)
	}
	if got, want := plan.Edges[2].ToolName, "toy-packager"; got != want {
		t.Fatalf("plan.Edges[2].ToolName = %q, want %q", got, want)
	}
	if got, want := plan.Edges[2].OutputType, "final"; got != want {
		t.Fatalf("plan.Edges[2].OutputType = %q, want %q", got, want)
	}
}

func TestPlannerPlanRepairsRejectedReview(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	locator, err := datadir.New(root)
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

	tool := spec.ToolSpec{
		Name:        "toy-finalizer",
		Version:     "0.1.0",
		Description: "final stage",
		InputTypes:  []string{"request"},
		OutputTypes: []string{"final"},
		Model:       "demo/toy-finalizer",
	}
	payload, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if err := store.UpsertTool(context.Background(), sqlite.ToolRecord{
		Name:       tool.Name,
		Image:      "host:///tmp/" + tool.Name,
		ConfigJSON: string(payload),
	}); err != nil {
		t.Fatalf("UpsertTool() error = %v", err)
	}

	planner := runner.NewPlanner(locator, store, staticExecutorPlanRuntime{payload: staticExecutorPlanJSON("Executor detail plan", "Refine and execute the assigned stage.", []string{"result.final"})}, staticPlannerClient(t,
		staticPlannerDraftJSON([]plannerDraftResponseEdge{{
			Ref:             "final",
			ToolName:        "toy-finalizer",
			Dependencies:    nil,
			InputType:       "request",
			OutputType:      "final",
			Title:           "Finalize task",
			Summary:         "Produce the final output.",
			ExpectedOutputs: []string{"result.final"},
			SubTask:         "Produce the final output for the request.",
		}}),
		[]byte(`{"approved":false}`),
		plannerApprovedJSON(),
	), nil)

	plan, err := planner.Plan(context.Background(), "task-456", taskflow.RequestEnvelope{Text: "finish the request"})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got, want := len(plan.Edges), 1; got != want {
		t.Fatalf("len(plan.Edges) = %d, want %d", got, want)
	}
	if plan.ReviewedAt.IsZero() {
		t.Fatal("plan.ReviewedAt is zero, want repaired review timestamp")
	}
}

type staticExecutorPlanRuntime struct {
	payload []byte
}

func (r staticExecutorPlanRuntime) Pull(context.Context, string) error {
	return nil
}

func (r staticExecutorPlanRuntime) DescribeTool(context.Context, string) ([]byte, error) {
	return nil, nil
}

func (r staticExecutorPlanRuntime) PlanExecutor(context.Context, runtime.ExecutorPlanRequest) ([]byte, error) {
	return append([]byte(nil), r.payload...), nil
}

func (r staticExecutorPlanRuntime) RunExecutor(context.Context, runtime.ExecutorRunRequest) error {
	return nil
}

type staticPlannerLLMClient struct {
	testing *testing.T
	mu      sync.Mutex
	index   int
	steps   [][]byte
}

func staticPlannerClient(t *testing.T, payloads ...[]byte) *staticPlannerLLMClient {
	t.Helper()
	steps := make([][]byte, 0, len(payloads))
	for _, payload := range payloads {
		steps = append(steps, append([]byte(nil), payload...))
	}
	return &staticPlannerLLMClient{testing: t, steps: steps}
}

func (c *staticPlannerLLMClient) CompleteJSON(context.Context, ai.Request) ([]byte, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.steps) == 0 {
		c.testing.Fatal("CompleteJSON() called with no prepared payloads")
	}
	if c.index >= len(c.steps) {
		c.testing.Fatalf("CompleteJSON() called more times than expected: got call %d with %d prepared payloads", c.index+1, len(c.steps))
	}
	payload := c.steps[c.index]
	c.index++
	return append([]byte(nil), payload...), "", nil
}

func staticPlannerDraftJSON(edges []plannerDraftResponseEdge) []byte {
	payload, err := json.Marshal(plannerDraftResponse{Mode: "linear", Edges: edges})
	if err != nil {
		panic(err)
	}
	return payload
}

func staticExecutorPlanJSON(summary, subTask string, outputs []string) []byte {
	payload, err := json.Marshal(spec.ExecutorPlan{
		Summary:         summary,
		SubTask:         subTask,
		ExpectedOutputs: outputs,
	})
	if err != nil {
		panic(err)
	}
	return payload
}

func plannerApprovedJSON() []byte {
	payload, err := json.Marshal(map[string]bool{"approved": true})
	if err != nil {
		panic(err)
	}
	return payload
}

func plannerReviewedPlanJSON(mode string, edges []spec.PlannedEdge) []byte {
	payload, err := json.Marshal(map[string]any{
		"plan": map[string]any{
			"mode":  mode,
			"edges": edges,
		},
	})
	if err != nil {
		panic(fmt.Errorf("marshal reviewed plan response: %w", err))
	}
	return payload
}
