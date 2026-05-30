package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tsumina/dango/llm"
	"github.com/tsumina/dango/stream"
)

func TestRunnerStoresHandoffMarkdownOutput(t *testing.T) {
	raw, err := (HandoffDoc{
		ChannelHeader: stream.ChannelHeader{
			RunnerID:  "runner-1",
			CreatedAt: time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		},
		FromNode: "node-1",
		ToNodes:  []string{"node-2"},
		Intent:   "continue",
		Body:     "output",
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
	workspace, err := ProvisionWorkspace(t.TempDir(), "runner-1", []string{"node-1"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	nodeWorkspace, ok := workspace.Skill("node-1")
	if !ok {
		t.Fatal("node workspace missing")
	}
	artifactPath := filepath.Join(nodeWorkspace.DownstreamDir, "artifacts", "predictions.csv")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(artifact parent): %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(artifact): %v", err)
	}
	raw, err := (HandoffDoc{
		ChannelHeader: stream.ChannelHeader{
			RunnerID:  "wrong-runner",
			CreatedAt: time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		},
		FromNode: "wrong-node",
		ToNodes:  []string{"node-2"},
		Intent:   "continue",
		Artifacts: []HandoffArtifact{{
			Path:        "downstream/artifacts/predictions.csv",
			Type:        HandoffArtifactFile,
			Description: "Prediction CSV",
		}},
		Body: "output",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	r.workspace = workspace
	handoffSub, err := r.SubscribeStream(stream.Filter{EventTypes: []string{stream.EventHandoffEmitted}}, stream.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("SubscribeStream(handoff): %v", err)
	}
	defer handoffSub.Cancel()
	sub, err := r.SubscribeStream(stream.Filter{EventTypes: []string{stream.EventArtifactCreated}}, stream.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("SubscribeStream(artifact): %v", err)
	}
	defer sub.Cancel()

	r.emitChannelDocumentEvents(context.Background(), &Node{Id: "node-1"}, raw)
	handoffEvent, ok, err := handoffSub.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("Next handoff event = %v/%v", ok, err)
	}
	var handoffDelta map[string]any
	if err := json.Unmarshal(handoffEvent.Delta, &handoffDelta); err != nil {
		t.Fatalf("Unmarshal handoff delta: %v", err)
	}
	if handoffDelta["runner_id"] != r.ID() || handoffDelta["from_node"] != "node-1" {
		t.Fatalf("handoff delta = %+v, want authoritative runner/node ids", handoffDelta)
	}

	event, ok, err := sub.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("Next artifact event = %v/%v", ok, err)
	}
	var delta map[string]any
	if err := json.Unmarshal(event.Delta, &delta); err != nil {
		t.Fatalf("Unmarshal delta: %v", err)
	}
	if delta["path"] != artifactPath || delta["declared_path"] != "downstream/artifacts/predictions.csv" || delta["resource_type"] != HandoffArtifactFile {
		t.Fatalf("delta = %+v, want resolved handoff artifact", delta)
	}
}

func TestRunnerEmitsExchangePublishedEvents(t *testing.T) {
	workspace, err := ProvisionWorkspace(t.TempDir(), "runner-1", []string{"node-1"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	raw, err := (ExchangeDoc{
		ChannelHeader: stream.ChannelHeader{
			RunnerID:  "wrong-runner",
			CreatedAt: time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		},
		NodeID: "wrong-node",
		Title:  "shared update",
		Body:   "published output",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	r.workspace = workspace
	sub, err := r.SubscribeStream(stream.Filter{EventTypes: []string{stream.EventExchangePublished}}, stream.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("SubscribeStream(exchange): %v", err)
	}
	defer sub.Cancel()

	r.emitChannelDocumentEvents(context.Background(), &Node{Id: "node-1"}, raw)

	event, ok, err := sub.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("Next exchange event = %v/%v", ok, err)
	}
	var delta map[string]any
	if err := json.Unmarshal(event.Delta, &delta); err != nil {
		t.Fatalf("Unmarshal delta: %v", err)
	}
	if delta["runner_id"] != r.ID() || delta["node_id"] != "node-1" {
		t.Fatalf("delta = %+v, want authoritative runner/node ids", delta)
	}
	if delta["path"] != workspace.ExchangeDir() || delta["title"] != "shared update" {
		t.Fatalf("delta = %+v, want exchange path and title", delta)
	}
}

func TestRunnerIgnoresMemoChannelOutputEvents(t *testing.T) {
	raw, err := (MemoDocument{
		ChannelHeader: stream.ChannelHeader{
			RunnerID:  "runner-1",
			CreatedAt: time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		},
		NodeID: "node-1",
		Path:   "memo/plan.md",
		Body:   "memo body",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	sub, err := r.SubscribeStream(stream.Filter{EventTypes: []string{stream.EventHandoffEmitted, stream.EventExchangePublished}}, stream.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("SubscribeStream(channel events): %v", err)
	}
	defer sub.Cancel()

	r.emitChannelDocumentEvents(context.Background(), &Node{Id: "node-1"}, raw)

	readCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, ok, err := sub.Next(readCtx)
	if !errors.Is(err, context.DeadlineExceeded) || ok {
		t.Fatalf("Next memo event = %v/%v, want deadline exceeded with no events", ok, err)
	}
}

func TestRunnerDeliversHandoffToSuccessorUpstreamDir(t *testing.T) {
	root := t.TempDir()
	workspace, err := ProvisionWorkspace(root, "runner-1", []string{"parent", "child"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	parentWS, ok := workspace.Skill("parent")
	if !ok {
		t.Fatal("parent workspace missing")
	}
	if err := os.WriteFile(filepath.Join(parentWS.DownstreamDir, "handoff.md"), []byte("handoff"), 0o644); err != nil {
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
	delivered := filepath.Join(childWS.UpstreamDir, "parent", "handoff.md")
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
		ChannelHeader: stream.ChannelHeader{RunnerID: "runner-1"},
		FromNode:      "parent",
		ToNodes:       []string{"child"},
		Artifacts: []HandoffArtifact{{
			Path: resourceFile,
			Type: HandoffArtifactFile,
		}},
		Body: "parent output",
	}).Markdown()
	if err == nil {
		t.Fatalf("Markdown unexpectedly accepted absolute artifact path: %s", parentOutput)
	}

	child := &resourceRecorderAgent{}
	parent := &Node{Id: "parent", Agent: &staticAgent{output: "parent output"}}
	childNode := &Node{Id: "child", Parents: []*Node{parent}, Agent: child}
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

func TestRunnerPassesRelativeHandoffArtifactDirsToChildBinder(t *testing.T) {
	workspace, err := ProvisionWorkspace(t.TempDir(), "runner-1", []string{"parent", "child"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	parentWorkspace, ok := workspace.Skill("parent")
	if !ok {
		t.Fatal("parent workspace missing")
	}
	artifactPath := filepath.Join(parentWorkspace.DownstreamDir, "artifacts", "predictions.csv")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(artifact parent): %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(artifact): %v", err)
	}
	parentOutput, err := (HandoffDoc{
		ChannelHeader: stream.ChannelHeader{RunnerID: "runner-1"},
		FromNode:      "parent",
		ToNodes:       []string{"child"},
		Artifacts: []HandoffArtifact{{
			Path: "downstream/artifacts/predictions.csv",
			Type: HandoffArtifactFile,
		}},
		Body: "parent output",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	r.workspace = workspace
	got := r.nodeAccessibleDirs("child", map[string]any{"parent": parentOutput})
	artifactDir, err := filepath.EvalSymlinks(filepath.Dir(artifactPath))
	if err != nil {
		t.Fatalf("EvalSymlinks(artifact dir): %v", err)
	}
	if !containsDir(got, artifactDir) {
		t.Fatalf("accessible dirs = %v, want artifact dir %s", got, artifactDir)
	}
}

type staticAgent struct {
	output any
}

func (e *staticAgent) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	return e.output, nil, nil
}

func (e *staticAgent) Polish(ctx context.Context) (any, error) { return nil, nil }

func (e *staticAgent) Report(ctx context.Context, output any) (any, error) { return nil, nil }

type resourceRecorderAgent struct {
	accessibleDirs []string
}

func (e *resourceRecorderAgent) BindForRunner(sessID *string, runtimePaths AgentRuntimePaths, sessStores ...llm.SessionStore) (string, error) {
	e.accessibleDirs = append([]string(nil), runtimePaths.AccessibleDirs...)
	return "", nil
}

func (e *resourceRecorderAgent) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	return "child output", nil, nil
}

func (e *resourceRecorderAgent) Polish(ctx context.Context) (any, error) { return nil, nil }

func (e *resourceRecorderAgent) Report(ctx context.Context, output any) (any, error) {
	return nil, nil
}
