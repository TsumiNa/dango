package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tsumina/dango/internal/llm"
)

func TestAnnotateExchangeOutputAddsRunnerAndNodeMetadata(t *testing.T) {
	raw, err := (ExchangeDocument{
		Stage:     ExchangeStageExecute,
		CreatedAt: time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		Handoff:   "output",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := New(WithLogger(testLogger))
	got := r.annotateExchangeOutput(&Node{
		Id:              "node-1",
		SkillName:       "skill-1",
		TaskDescription: "Do the thing.",
	}, raw)

	text, ok := got.(string)
	if !ok {
		t.Fatalf("annotated output type = %T, want string", got)
	}
	parsed, err := ParseExchangeMarkdown(text)
	if err != nil {
		t.Fatalf("ParseExchangeMarkdown: %v", err)
	}
	if parsed.RunnerID != r.ID() || parsed.NodeID != "node-1" || parsed.SkillName != "skill-1" || parsed.TaskDescription != "Do the thing." {
		t.Fatalf("metadata = %+v, want runner/node metadata", parsed)
	}
}

func TestExchangeResourcesSurviveAnnotation(t *testing.T) {
	resourceDir := t.TempDir()
	raw, err := (ExchangeDocument{
		Stage: ExchangeStageExecute,
		Resources: []ExchangeResource{{
			Path:        filepath.Join(resourceDir, "predictions.csv"),
			Type:        ExchangeResourceFile,
			Description: "Prediction CSV",
		}},
		Handoff: "output",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := New(WithLogger(testLogger))
	got := r.annotateExchangeOutput(&Node{Id: "node-1"}, raw)
	parsed, err := ParseExchangeMarkdown(got.(string))
	if err != nil {
		t.Fatalf("ParseExchangeMarkdown: %v", err)
	}
	if len(parsed.Resources) != 1 || parsed.Resources[0].Path != filepath.Join(resourceDir, "predictions.csv") {
		t.Fatalf("resources = %+v, want prediction resource", parsed.Resources)
	}
}

func TestRunnerPassesParentExchangeResourceDirsToChildBinder(t *testing.T) {
	resourceDir := t.TempDir()
	resourceFile := filepath.Join(resourceDir, "predictions.csv")
	if err := os.WriteFile(resourceFile, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write resource: %v", err)
	}
	parentOutput, err := (ExchangeDocument{
		Stage: ExchangeStageExecute,
		Resources: []ExchangeResource{{
			Path: resourceFile,
			Type: ExchangeResourceFile,
		}},
		Handoff: "parent output",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	child := &resourceRecorderExecutor{}
	parent := &Node{Id: "parent", Executor: &staticExecutor{output: parentOutput}}
	childNode := &Node{Id: "child", Parents: []*Node{parent}, Executor: child}
	r := New(WithLogger(testLogger))
	if err := r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.AddNodes(context.Background(), parent, childNode); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}
	waitForRunnerEvent(t, r, EventEngineIdle, "")
	if err := r.Complete(context.Background()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := r.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	realResourceDir, err := filepath.EvalSymlinks(resourceDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(resourceDir): %v", err)
	}
	if len(child.accessibleDirs) != 1 || child.accessibleDirs[0] != realResourceDir {
		t.Fatalf("child accessible dirs = %v, want [%s]", child.accessibleDirs, realResourceDir)
	}
}

func TestAnnotateExchangeOutputLeavesPlainValuesUntouched(t *testing.T) {
	r := New(WithLogger(testLogger))
	if got := r.annotateExchangeOutput(&Node{Id: "node-1"}, 10); got != 10 {
		t.Fatalf("annotate int = %v, want 10", got)
	}
	if got := r.annotateExchangeOutput(&Node{Id: "node-1"}, "plain"); got != "plain" {
		t.Fatalf("annotate plain string = %v, want plain", got)
	}
}

type staticExecutor struct {
	output any
}

func (e *staticExecutor) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	return e.output, nil, nil
}

func (e *staticExecutor) Polish(ctx context.Context) (any, error) { return nil, nil }

func (e *staticExecutor) Report(ctx context.Context, output any) (any, error) { return nil, nil }

type resourceRecorderExecutor struct {
	accessibleDirs []string
}

func (e *resourceRecorderExecutor) BindForRunner(sessID *string, accessibleDirs []string, sessStores ...llm.SessionStore) (string, error) {
	e.accessibleDirs = append([]string(nil), accessibleDirs...)
	return "", nil
}

func (e *resourceRecorderExecutor) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	return "child output", nil, nil
}

func (e *resourceRecorderExecutor) Polish(ctx context.Context) (any, error) { return nil, nil }

func (e *resourceRecorderExecutor) Report(ctx context.Context, output any) (any, error) {
	return nil, nil
}
