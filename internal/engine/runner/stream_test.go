package runner

import (
	"context"
	"encoding/json"
	"errors"
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
		if filter.Match(event) {
			return event, true
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

func TestRunnerNodeAddedEventIncludesDescribeFields(t *testing.T) {
	r := newTestRunner()
	sub, err := r.SubscribeStream(streampkg.Filter{EventTypes: []string{streampkg.EventRunnerNodeAdded}}, streampkg.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("SubscribeStream: %v", err)
	}
	defer sub.Cancel()

	parent := &Node{
		Id:              "plan",
		SkillName:       "planner",
		TaskDescription: "Draft a plan",
		Executor: &testExecutor{
			run: func(context.Context, map[string]any) (any, []*Node, error) {
				return "planned", nil, nil
			},
		},
	}
	child := &Node{
		Id:              "report",
		SkillName:       "reporter",
		TaskDescription: "Write the report",
		Parents:         []*Node{parent},
		Executor: &testExecutor{
			run: func(context.Context, map[string]any) (any, []*Node, error) {
				return "reported", nil, nil
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.AddNodes(ctx, parent, child); err != nil {
		t.Fatalf("AddNodes: %v", err)
	}

	event, ok := nextExpandedStreamEventWithin(t, sub, streampkg.Filter{
		EventTypes: []string{streampkg.EventRunnerNodeAdded},
		Scope:      streampkg.Scope{RunnerID: r.ID(), NodeID: child.Id},
	}, 2*time.Second)
	if !ok {
		t.Fatal("stream closed before child node-added event")
	}

	var delta map[string]any
	if err := json.Unmarshal(event.Delta, &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	if delta["node_id"] != child.Id {
		t.Fatalf("delta node_id = %v, want %q", delta["node_id"], child.Id)
	}
	if delta["skill_name"] != child.SkillName {
		t.Fatalf("delta skill_name = %v, want %q", delta["skill_name"], child.SkillName)
	}
	if delta["task_description"] != child.TaskDescription {
		t.Fatalf("delta task_description = %v, want %q", delta["task_description"], child.TaskDescription)
	}
	dependsOn, ok := delta["depends_on"].([]any)
	if !ok {
		t.Fatalf("delta depends_on = %T, want []any", delta["depends_on"])
	}
	if len(dependsOn) != 1 || dependsOn[0] != parent.Id {
		t.Fatalf("delta depends_on = %v, want [%q]", dependsOn, parent.Id)
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

func TestRunnerEmitsArtifactCreatedEventsFromHandoffOutput(t *testing.T) {
	raw, err := (HandoffDoc{
		RunnerID: "runner-1",
		FromNode: "artifact-node",
		ToNodes:  []string{"downstream"},
		Intent:   "continue",
		Artifacts: []HandoffArtifact{{
			Path:        "outbox/artifacts/predictions.csv",
			Type:        HandoffArtifactFile,
			Description: "prediction table",
		}},
		Body: "done",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	eventStream := r.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventArtifactCreated}}, streampkg.WithSubscriberBuffer(8))
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
	if delta["path"] != "outbox/artifacts/predictions.csv" {
		t.Fatalf("delta path = %v", delta["path"])
	}
	if delta["resource_type"] != HandoffArtifactFile {
		t.Fatalf("delta resource_type = %v, want %q", delta["resource_type"], HandoffArtifactFile)
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

func TestRunnerEmitsHandoffEventsFromHandoffOutput(t *testing.T) {
	raw, err := (HandoffDoc{
		RunnerID: "runner-1",
		FromNode: "handoff-node",
		ToNodes:  []string{"downstream"},
		Intent:   "continue",
		Body:     "Parsed inputs and prepared durable outputs.",
	}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	eventStream := r.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventHandoffEmitted}}, streampkg.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	node := &Node{
		Id:        "handoff-node",
		SkillName: "handoff-skill",
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

	event, ok := nextExpandedStreamEventWithin(t, sub, streampkg.Filter{EventTypes: []string{streampkg.EventHandoffEmitted}}, 2*time.Second)
	if !ok {
		t.Fatal("stream closed before handoff event")
	}
	if event.From.Layer != "skill" || event.From.ID != "handoff-skill" || event.From.ParentID != node.Id {
		t.Fatalf("event source = %+v, want skill handoff source with node parent", event.From)
	}
	if event.Scope.RunnerID != r.ID() || event.Scope.NodeID != node.Id {
		t.Fatalf("event scope = %+v, want runner/node ids", event.Scope)
	}
	var delta map[string]any
	if err := json.Unmarshal(event.Delta, &delta); err != nil {
		t.Fatalf("unmarshal delta: %v", err)
	}
	if delta["document"] != "Parsed inputs and prepared durable outputs." {
		t.Fatalf("delta document = %v", delta["document"])
	}
	if delta["intent"] != "continue" {
		t.Fatalf("delta intent = %v, want continue", delta["intent"])
	}
	if delta["node_id"] != node.Id {
		t.Fatalf("delta node_id = %v, want %q", delta["node_id"], node.Id)
	}
	if got := event.Metadata["skill_name"]; got != "handoff-skill" {
		t.Fatalf("event metadata skill_name = %v, want handoff-skill", got)
	}
}

func TestRunnerDoesNotEmitSkillMemoWithoutMemo(t *testing.T) {
	raw, err := (HandoffDoc{RunnerID: "runner-1", FromNode: "no-memo", ToNodes: []string{"downstream"}, Body: "done"}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	eventStream := r.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventSkillMemoDelta}}, streampkg.WithSubscriberBuffer(8))
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
	raw, err := (HandoffDoc{RunnerID: "runner-1", FromNode: "no-artifact", ToNodes: []string{"downstream"}, Body: "done"}).Markdown()
	if err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	r := newTestRunner()
	eventStream := r.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventArtifactCreated}}, streampkg.WithSubscriberBuffer(8))
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
