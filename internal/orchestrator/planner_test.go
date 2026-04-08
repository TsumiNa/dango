package orchestrator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tsumina/dango/internal/layout"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
)

func TestPlannerPlanBuildsLinearDemoPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	layout, err := layout.New(root)
	if err != nil {
		t.Fatalf("layout.New() error = %v", err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatalf("layout.Ensure() error = %v", err)
	}

	store, err := sqlite.Open(layout.DBPath())
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

	planner := NewPlanner(store, nil)
	plan, err := planner.Plan(context.Background(), "task-123", "write a small demo")
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
