package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
)

func TestRunnerStoresHandoffMarkdownOutput(t *testing.T) {
	raw, err := (HandoffDoc{
		RunnerID:  "runner-1",
		FromNode:  "node-1",
		ToNodes:   []string{"node-2"},
		Intent:    "continue",
		CreatedAt: time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		Body:      "output",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	stored := newStoredRunnerEvent(RunnerEvent{Type: EventNodeCompleted, NodeID: "node-1", Data: raw})
	if stored.DataEncoding != "markdown" || stored.DataText != raw {
		t.Fatalf("stored event = %+v, want markdown handoff", stored)
	}
}

func TestRunnerEmitsHandoffArtifactEvents(t *testing.T) {
	raw, err := (HandoffDoc{
		RunnerID: "runner-1",
		FromNode: "node-1",
		ToNodes:  []string{"node-2"},
		Intent:   "continue",
		Artifacts: []HandoffArtifact{{
			Path:        "outbox/artifacts/predictions.csv",
			Type:        HandoffArtifactFile,
			Description: "Prediction CSV",
		}},
		CreatedAt: time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		Body:      "output",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	sub, err := r.SubscribeStream(stream.Filter{EventTypes: []string{stream.EventArtifactCreated}}, stream.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("SubscribeStream: %v", err)
	}
	defer sub.Cancel()

	r.emitChannelDocumentEvents(context.Background(), &Node{Id: "node-1"}, raw)
	event, ok, err := sub.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("Next artifact event = %v/%v", ok, err)
	}
	var delta map[string]any
	if err := json.Unmarshal(event.Delta, &delta); err != nil {
		t.Fatalf("Unmarshal delta: %v", err)
	}
	if delta["path"] != "outbox/artifacts/predictions.csv" || delta["resource_type"] != HandoffArtifactFile {
		t.Fatalf("delta = %+v, want handoff artifact", delta)
	}
}

func TestRunnerDeliversHandoffToSuccessorInbox(t *testing.T) {
	root := t.TempDir()
	workspace, err := ProvisionWorkspace(root, "runner-1", []string{"parent", "child"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	parentWS, ok := workspace.Skill("parent")
	if !ok {
		t.Fatal("parent workspace missing")
	}
	if err := os.WriteFile(filepath.Join(parentWS.OutboxDir, "handoff.md"), []byte("handoff"), 0o644); err != nil {
		t.Fatalf("write handoff: %v", err)
	}

	r := newTestRunner()
	r.workspace = workspace
	if err := r.deliverHandoffToSuccessor(context.Background(), &Node{Id: "parent"}, &Node{Id: "child"}); err != nil {
		t.Fatalf("deliverHandoffToSuccessor: %v", err)
	}
	childWS, ok := workspace.Skill("child")
	if !ok {
		t.Fatal("child workspace missing")
	}
	delivered := filepath.Join(childWS.InboxDir, "parent", "handoff.md")
	if _, err := os.Lstat(delivered); err != nil {
		t.Fatalf("delivered handoff stat: %v", err)
	}
}

func TestRunnerSkipsExternalArtifactsWithoutTrustedRoots(t *testing.T) {
	resourceDir := t.TempDir()
	resourceFile := filepath.Join(resourceDir, "predictions.csv")
	if err := os.WriteFile(resourceFile, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write resource: %v", err)
	}
	parentOutput, err := (HandoffDoc{
		RunnerID: "runner-1",
		FromNode: "parent",
		ToNodes:  []string{"child"},
		Artifacts: []HandoffArtifact{{
			Path: resourceFile,
			Type: HandoffArtifactFile,
		}},
		Body: "parent output",
	}).Markdown()
	if err == nil {
		t.Fatalf("Markdown unexpectedly accepted absolute artifact path: %s", parentOutput)
	}

	child := &resourceRecorderExecutor{}
	parent := &Node{Id: "parent", Executor: &staticExecutor{output: "parent output"}}
	childNode := &Node{Id: "child", Parents: []*Node{parent}, Executor: child}
	r := newTestRunner()
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
	if len(child.accessibleDirs) != 0 {
		t.Fatalf("child accessible dirs = %v, want none for external artifacts", child.accessibleDirs)
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
