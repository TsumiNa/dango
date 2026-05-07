package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

func nextExpandedStreamEventWithin(t *testing.T, sub *streampkg.Subscription, filter streampkg.Filter, timeout time.Duration) (streampkg.Event, bool) {
	t.Helper()
	readCtx, cancelRead := context.WithTimeout(context.Background(), timeout)
	defer cancelRead()
	for {
		event, ok, err := sub.Next(readCtx)
		if errors.Is(err, context.DeadlineExceeded) {
			return streampkg.Event{}, false
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			t.Fatal("stream closed before expanded event")
		}
		events, err := streampkg.FilterBundleEvent(event, filter)
		if err != nil {
			t.Fatalf("FilterBundleEvent: %v", err)
		}
		if len(events) > 0 {
			return events[0], true
		}
	}
}

func TestSubscribeStreamReplaysRunnerOwnedPhaseEvents(t *testing.T) {
	r := newTestRunner()
	r.stateMu.Lock()
	r.phase = PhasePolishing
	r.stateMu.Unlock()
	r.emitPhaseChangedEvent()

	sub, err := r.SubscribeStream(streampkg.Filter{EventTypes: []string{streampkg.EventRunnerPhaseChanged}}, streampkg.WithSubscriberBuffer(1))
	if err != nil {
		t.Fatalf("SubscribeStream: %v", err)
	}
	defer sub.Cancel()

	readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	event, ok, err := sub.Next(readCtx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !ok {
		t.Fatal("stream closed before replaying phase event")
	}
	var delta map[string]string
	if err := json.Unmarshal(event.Delta, &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	if event.Scope.RunnerID != r.ID() || delta["phase"] != string(PhasePolishing) {
		t.Fatalf("phase event scope=%+v delta=%v, want runner-owned polishing replay", event.Scope, delta)
	}
}

func TestRunnerDoneClosesViaStreamSettleEvent(t *testing.T) {
	r := newTestRunner()

	select {
	case <-r.Done():
		t.Fatal("Done channel closed before settle")
	case <-time.After(20 * time.Millisecond):
	}

	r.Abort(context.Canceled)

	select {
	case <-r.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done channel did not close after Abort settle event")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Wait(waitCtx); err == nil {
		t.Fatal("Wait returned nil, want canceled engine error")
	}
}

func TestRunnerEmitsCompactStreamEvents(t *testing.T) {
	r := newTestRunner()
	eventStream := r.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventRunnerNodeCompleted}}, streampkg.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	node := &Node{
		Id: "compact",
		Executor: &testExecutor{
			run: func(context.Context, map[string]any) (any, []*Node, error) {
				return strings.Repeat("x", 1024), nil, nil
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.AddNodes(ctx, node); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}

	readCtx, cancelRead := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRead()
	event, ok, err := sub.Next(readCtx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !ok {
		t.Fatal("stream closed before node completion")
	}
	if event.Scope.RunnerID != r.ID() || event.Scope.NodeID != "compact" {
		t.Fatalf("event scope = %+v, want runner/node ids", event.Scope)
	}
	var delta map[string]any
	if err := json.Unmarshal(event.Delta, &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	if delta["node_id"] != "compact" {
		t.Fatalf("delta node_id = %v, want compact", delta["node_id"])
	}
	if strings.Contains(string(event.Delta), strings.Repeat("x", 64)) {
		t.Fatalf("runner stream event leaked node output: %s", event.Delta)
	}
}

func TestRunnerEngineStoppedAfterIdleIsCompletedStreamStatus(t *testing.T) {
	r := newTestRunner()
	sub, err := r.SubscribeStream(streampkg.Filter{EventTypes: []string{streampkg.EventStatusProgress}}, streampkg.WithSubscriberBuffer(16))
	if err != nil {
		t.Fatalf("SubscribeStream: %v", err)
	}
	defer sub.Cancel()

	ctx, cancel := context.WithCancel(context.Background())
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	node := &Node{
		Id: "idle-node",
		Executor: &testExecutor{
			run: func(context.Context, map[string]any) (any, []*Node, error) {
				return "done", nil, nil
			},
		},
	}
	if err := r.AddNodes(ctx, node); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}
	waitForRunnerEvent(t, r, EventEngineIdle, "")
	cancel()

	readCtx, cancelRead := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRead()
	for {
		event, ok, err := sub.Next(readCtx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			t.Fatal("stream closed before engine stopped event")
		}
		var delta map[string]string
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			t.Fatalf("unmarshal delta: %v", err)
		}
		if delta["event"] != EventEngineStopped.String() {
			continue
		}
		if event.Status != streampkg.StatusCompleted {
			t.Fatalf("EngineStopped status = %q, want %q", event.Status, streampkg.StatusCompleted)
		}
		return
	}
}

func TestRunnerEmitsArtifactCreatedEventsFromExchangeOutput(t *testing.T) {
	artifactPath := filepath.Join(t.TempDir(), "predictions.csv")
	if err := os.WriteFile(artifactPath, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	raw, err := (ExchangeDocument{
		Stage: ExchangeStageExecute,
		Resources: []ExchangeResource{{
			Path:        artifactPath,
			Type:        ExchangeResourceFile,
			Description: "prediction table",
		}},
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	eventStream := r.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventArtifactCreated, streampkg.EventMergeBundle}}, streampkg.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	node := &Node{
		Id: "artifact-node",
		Executor: &testExecutor{
			run: func(context.Context, map[string]any) (any, []*Node, error) {
				return raw, nil, nil
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.AddNodes(ctx, node); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}

	event, ok := nextExpandedStreamEventWithin(t, sub, streampkg.Filter{EventTypes: []string{streampkg.EventArtifactCreated}}, 2*time.Second)
	if !ok {
		t.Fatal("stream closed before artifact event")
	}
	if event.From.Layer != "executor" {
		t.Fatalf("event source layer = %q, want executor", event.From.Layer)
	}
	if event.Scope.RunnerID != r.ID() || event.Scope.NodeID != node.Id {
		t.Fatalf("event scope = %+v, want runner/node ids", event.Scope)
	}
	var delta map[string]any
	if err := json.Unmarshal(event.Delta, &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	if delta["path"] != artifactPath {
		t.Fatalf("delta path = %v, want %q", delta["path"], artifactPath)
	}
	if delta["resource_type"] != ExchangeResourceFile {
		t.Fatalf("delta resource_type = %v, want %q", delta["resource_type"], ExchangeResourceFile)
	}
	if delta["stage"] != string(ExchangeStageExecute) {
		t.Fatalf("delta stage = %v, want %q", delta["stage"], ExchangeStageExecute)
	}
	if delta["description"] != "prediction table" {
		t.Fatalf("delta description = %v, want prediction table", delta["description"])
	}
	if got := event.Metadata["runner_id"]; got != r.ID() {
		t.Fatalf("event metadata runner_id = %v, want %q", got, r.ID())
	}
	if got := event.Metadata["node_id"]; got != node.Id {
		t.Fatalf("event metadata node_id = %v, want %q", got, node.Id)
	}
}

func TestRunnerEmitsSkillMemoEventsFromExchangeOutput(t *testing.T) {
	raw, err := (ExchangeDocument{
		Stage:   ExchangeStageExecute,
		Memo:    "Parsed inputs and prepared durable outputs.",
		Handoff: "done",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	eventStream := r.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventSkillMemoDelta, streampkg.EventMergeBundle}}, streampkg.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	node := &Node{
		Id:        "memo-node",
		SkillName: "memo-skill",
		Executor: &testExecutor{
			run: func(context.Context, map[string]any) (any, []*Node, error) {
				return raw, nil, nil
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.AddNodes(ctx, node); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}

	event, ok := nextExpandedStreamEventWithin(t, sub, streampkg.Filter{EventTypes: []string{streampkg.EventSkillMemoDelta}}, 2*time.Second)
	if !ok {
		t.Fatal("stream closed before memo event")
	}
	if event.From.Layer != "skill" || event.From.ID != "memo-skill" || event.From.ParentID != node.Id {
		t.Fatalf("event source = %+v, want skill memo source with node parent", event.From)
	}
	if event.Scope.RunnerID != r.ID() || event.Scope.NodeID != node.Id {
		t.Fatalf("event scope = %+v, want runner/node ids", event.Scope)
	}
	var delta map[string]any
	if err := json.Unmarshal(event.Delta, &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	if delta["memo"] != "Parsed inputs and prepared durable outputs." {
		t.Fatalf("delta memo = %v", delta["memo"])
	}
	if delta["stage"] != string(ExchangeStageExecute) {
		t.Fatalf("delta stage = %v, want %q", delta["stage"], ExchangeStageExecute)
	}
	if delta["node_id"] != node.Id {
		t.Fatalf("delta node_id = %v, want %q", delta["node_id"], node.Id)
	}
	if got := event.Metadata["skill_name"]; got != "memo-skill" {
		t.Fatalf("event metadata skill_name = %v, want memo-skill", got)
	}
}

func TestRunnerDoesNotEmitSkillMemoWithoutMemo(t *testing.T) {
	raw, err := (ExchangeDocument{Stage: ExchangeStageExecute, Handoff: "done"}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	eventStream := r.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventSkillMemoDelta, streampkg.EventMergeBundle}}, streampkg.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	node := &Node{
		Id: "no-memo",
		Executor: &testExecutor{
			run: func(context.Context, map[string]any) (any, []*Node, error) {
				return raw, nil, nil
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.AddNodes(ctx, node); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}
	waitForRunnerEvent(t, r, EventNodeCompleted, node.Id)

	if event, ok := nextExpandedStreamEventWithin(t, sub, streampkg.Filter{EventTypes: []string{streampkg.EventSkillMemoDelta}}, 100*time.Millisecond); ok {
		t.Fatalf("unexpected skill memo event: %+v", event)
	}
}

func TestRunnerDoesNotEmitArtifactCreatedWithoutResources(t *testing.T) {
	raw, err := (ExchangeDocument{Stage: ExchangeStageExecute, Handoff: "done"}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	eventStream := r.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventArtifactCreated, streampkg.EventMergeBundle}}, streampkg.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	node := &Node{
		Id: "no-artifact",
		Executor: &testExecutor{
			run: func(context.Context, map[string]any) (any, []*Node, error) {
				return raw, nil, nil
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.AddNodes(ctx, node); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}
	waitForRunnerEvent(t, r, EventNodeCompleted, node.Id)

	if event, ok := nextExpandedStreamEventWithin(t, sub, streampkg.Filter{EventTypes: []string{streampkg.EventArtifactCreated}}, 100*time.Millisecond); ok {
		t.Fatalf("unexpected artifact event: %+v", event)
	}
}
