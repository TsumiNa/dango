package runner

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
)

type bindRecorderExecutor struct {
	calls       int
	seenSession []string
}

func (e *bindRecorderExecutor) BindForRunner(sessID *string, accessibleDirs []string, sessStores ...llm.SessionStore) (string, error) {
	e.calls++
	if len(sessStores) != 1 || sessStores[0] == nil {
		return "", context.Canceled
	}
	if sessID != nil {
		e.seenSession = append(e.seenSession, *sessID)
		return *sessID, nil
	}
	e.seenSession = append(e.seenSession, "")
	return "session-1", nil
}

func (e *bindRecorderExecutor) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	return nil, nil, nil
}

func (e *bindRecorderExecutor) Polish(ctx context.Context) (any, error) { return nil, nil }

func (e *bindRecorderExecutor) Report(ctx context.Context, output any) (any, error) {
	return output, nil
}

func TestRunner_PrepareNodeExecutors_ReusesStoredSessionID(t *testing.T) {
	exec := &bindRecorderExecutor{}
	r := New(
		WithLogger(testLogger),
		WithInitialPlan(&CoarsePlan{Request: "demo"}, map[string]*Node{
			"only": {Id: "only", Executor: exec},
		}),
	)

	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}
	if exec.calls != 1 {
		t.Fatalf("bind calls after StartPolish = %d, want 1", exec.calls)
	}
	if got := exec.seenSession[0]; got != "" {
		t.Fatalf("first bind session = %q, want empty", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached awaiting review, got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := r.AcceptPolishedPlan(context.Background(), &CoarsePlan{Request: "demo"}); err != nil {
		t.Fatalf("AcceptPolishedPlan: %v", err)
	}
	if exec.calls != 2 {
		t.Fatalf("bind calls after AcceptPolishedPlan = %d, want 2", exec.calls)
	}
	if got := exec.seenSession[1]; got != "session-1" {
		t.Fatalf("second bind session = %q, want session-1", got)
	}
}

type streamingBindExecutor struct {
	eventStream *streampkg.Stream
}

func (e *streamingBindExecutor) BindForRunner(sessID *string, accessibleDirs []string, sessStores ...llm.SessionStore) (string, error) {
	e.eventStream = streampkg.New(streampkg.Scope{NodeID: "owned-node"}, streampkg.DefaultConfig())
	return "session-owned-stream", nil
}

func (e *streamingBindExecutor) EventStream() *streampkg.Stream { return e.eventStream }

func (e *streamingBindExecutor) Execute(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
	return nil, nil, nil
}

func (e *streamingBindExecutor) Polish(ctx context.Context) (any, error) { return nil, nil }

func (e *streamingBindExecutor) Report(ctx context.Context, output any) (any, error) {
	return output, nil
}

func TestRunner_PrepareNodeExecutor_MergesExecutorOwnedStream(t *testing.T) {
	exec := &streamingBindExecutor{}
	r := newTestRunner()
	sub, err := r.SubscribeStream(streampkg.Filter{}, streampkg.WithSubscriberBuffer(4))
	if err != nil {
		t.Fatalf("SubscribeStream: %v", err)
	}
	defer sub.Cancel()

	if err := r.prepareNodeExecutor("owned-node", exec, nil); err != nil {
		t.Fatalf("prepareNodeExecutor: %v", err)
	}
	if exec.eventStream == nil {
		t.Fatal("executor did not create an event stream during bind")
	}
	if err := exec.eventStream.Emit(context.Background(), streampkg.Event{
		EventType: streampkg.EventLLMOutputDelta,
		From:      streampkg.Source{Layer: "skill", ID: "owned-skill"},
		Status:    streampkg.StatusRunning,
		Scope:     streampkg.Scope{NodeID: "owned-node"},
		Delta:     json.RawMessage(`"hello"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	topEvent, ok, err := sub.Next(ctx)
	if err != nil || !ok {
		t.Fatalf("Next = (_, %v, %v), want merged event", ok, err)
	}
	if topEvent.EventType != streampkg.EventMergeBundle {
		t.Fatalf("merged top event = %q, want bundle", topEvent.EventType)
	}
	events, err := streampkg.FilterBundleEvent(topEvent, streampkg.Filter{EventTypes: []string{streampkg.EventLLMOutputDelta}})
	if err != nil {
		t.Fatalf("FilterBundleEvent: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expanded events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Scope.RunnerID != r.ID() || event.Scope.NodeID != "owned-node" || event.From.ID != "owned-skill" {
		t.Fatalf("merged event = %+v, want runner and node scope preserved", event)
	}
}
