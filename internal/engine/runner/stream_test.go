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

func TestSubscribeStreamRequiresConfiguredStream(t *testing.T) {
	r := New(WithLogger(testLogger))
	if _, err := r.SubscribeStream(streampkg.Filter{}); !errors.Is(err, ErrStreamNotConfigured) {
		t.Fatalf("SubscribeStream err = %v, want ErrStreamNotConfigured", err)
	}
}

func TestRunnerEmitsCompactStreamEvents(t *testing.T) {
	eventStream := streampkg.New(streampkg.Scope{RequestID: "req_runner"})
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventRunnerNodeCompleted}}, streampkg.WithSubscriberBuffer(8))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	r := New(WithLogger(testLogger), WithStream(eventStream))
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
