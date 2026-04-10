package orchestrator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
)

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

	planner := NewPlannerWithClient(locator, store, nil, staticPlannerClient(t, staticPlannerDraftJSON(
		[]plannerDraftResponseEdge{
			{Ref: "brief", ToolName: "toy-brief", Dependencies: nil, InputType: "request", OutputType: "brief", Title: "Create brief", Summary: "Produce a brief from the request.", ExpectedOutputs: []string{"brief.md"}, SubTask: "Read the request and produce a short brief artifact."},
			{Ref: "draft", ToolName: "toy-drafter", Dependencies: []string{"brief"}, InputType: "brief", OutputType: "draft", Title: "Draft content", Summary: "Expand the brief into a draft.", ExpectedOutputs: []string{"draft.md"}, SubTask: "Use the brief input and produce a draft output."},
			{Ref: "final", ToolName: "toy-packager", Dependencies: []string{"draft"}, InputType: "draft", OutputType: "final", Title: "Package result", Summary: "Turn the draft into the final artifact.", ExpectedOutputs: []string{"final-report.md"}, SubTask: "Package the draft into final deliverables."},
		},
	)), nil)
	plan, err := planner.Plan(context.Background(), "task-123", "write a small project status update")
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

type staticPlannerLLMClient struct {
	testing *testing.T
	payload []byte
}

func staticPlannerClient(t *testing.T, payload []byte) staticPlannerLLMClient {
	t.Helper()
	return staticPlannerLLMClient{testing: t, payload: payload}
}

func (c staticPlannerLLMClient) CompleteJSON(context.Context, llm.Request) ([]byte, error) {
	return append([]byte(nil), c.payload...), nil
}

func staticPlannerDraftJSON(edges []plannerDraftResponseEdge) []byte {
	payload, err := json.Marshal(plannerDraftResponse{Mode: "linear", Edges: edges})
	if err != nil {
		panic(err)
	}
	return payload
}
