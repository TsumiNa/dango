package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestStreamEmitDeliversToMultipleSubscribers(t *testing.T) {
	s := New(Scope{RequestID: "req_1", RunnerID: "run_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	first, err := s.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe first: %v", err)
	}
	second, err := s.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe second: %v", err)
	}

	err = s.Emit(t.Context(), Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "orchestrator", ID: "or_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"planning"`),
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	for name, sub := range map[string]*Subscription{"first": first, "second": second} {
		got := receiveEvent(t, sub.Events())
		if got.SequenceNumber != 1 {
			t.Errorf("%s sequence = %d, want 1", name, got.SequenceNumber)
		}
		if got.Scope.RequestID != "req_1" || got.Scope.RunnerID != "run_1" {
			t.Errorf("%s scope = %+v, want stream scope", name, got.Scope)
		}
		if string(got.Delta) != `"planning"` {
			t.Errorf("%s delta = %s, want planning string", name, got.Delta)
		}
	}
}

func TestStreamSubscribeFiltersAndReplay(t *testing.T) {
	s := New(Scope{RequestID: "req_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	emit := func(eventType string, delta string) {
		t.Helper()
		if err := s.Emit(t.Context(), Event{
			EventType: eventType,
			From:      Source{Layer: "conversation"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(delta),
		}); err != nil {
			t.Fatalf("Emit %s: %v", eventType, err)
		}
	}
	emit(EventStatusProgress, `"status"`)
	emit(EventLLMReasoningDelta, `"think"`)
	emit(EventLLMOutputDelta, `"answer"`)

	sub, err := s.Subscribe(Filter{Prefixes: []string{"llm."}}, WithReplayFrom(2))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	first := receiveEvent(t, sub.Events())
	second := receiveEvent(t, sub.Events())
	if first.SequenceNumber != 2 || first.EventType != EventLLMReasoningDelta {
		t.Fatalf("first replay = seq %d type %s, want seq 2 reasoning", first.SequenceNumber, first.EventType)
	}
	if second.SequenceNumber != 3 || second.EventType != EventLLMOutputDelta {
		t.Fatalf("second replay = seq %d type %s, want seq 3 output", second.SequenceNumber, second.EventType)
	}
	assertNoEvent(t, sub.Events())
}

func TestSubscriptionCancelStopsDelivery(t *testing.T) {
	s := New(Scope{}, DefaultConfig())
	t.Cleanup(s.Close)

	sub, err := s.Subscribe(Filter{}, WithSubscriberBuffer(0))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	sub.Cancel()
	assertClosed(t, sub.Events())

	if err := s.Emit(t.Context(), Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "runner"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"ignored"`),
	}); err != nil {
		t.Fatalf("Emit after cancel: %v", err)
	}
}

func TestSubscriptionSlowConsumerDoesNotBlockEmit(t *testing.T) {
	s := New(Scope{}, DefaultConfig())
	t.Cleanup(s.Close)

	sub, err := s.Subscribe(Filter{}, WithSubscriberBuffer(0))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Emit(context.Background(), Event{
			EventType: EventStatusProgress,
			From:      Source{Layer: "runner"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(`"blocked"`),
		})
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Emit err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Emit did not unblock after subscription cancel")
	}
}

func TestSubscriptionOverflowErrorPolicy(t *testing.T) {
	s := New(Scope{}, DefaultConfig())
	t.Cleanup(s.Close)

	sub, err := s.Subscribe(Filter{}, WithSubscriberBuffer(0), WithOverflowPolicy(OverflowError))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	err = s.Emit(context.Background(), Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "runner"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"blocked"`),
	})
	if !errors.Is(err, ErrSubscriberOverflow) {
		t.Fatalf("Emit err = %v, want ErrSubscriberOverflow", err)
	}
}

func TestStreamEmitRejectsInvalidEvents(t *testing.T) {
	s := New(Scope{}, DefaultConfig())
	t.Cleanup(s.Close)

	err := s.Emit(t.Context(), Event{
		From:   Source{Layer: "orchestrator"},
		Status: StatusRunning,
		Delta:  json.RawMessage(`"missing type"`),
	})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Emit err = %v, want ErrInvalidEvent", err)
	}
}

func TestStreamCloseClosesSubscribersAndRejectsUse(t *testing.T) {
	s := New(Scope{}, DefaultConfig())
	sub, err := s.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	s.Close()
	assertClosed(t, sub.Events())

	if err := s.Emit(t.Context(), Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "orchestrator"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"closed"`),
	}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Emit err = %v, want ErrClosed", err)
	}
	if _, err := s.Subscribe(Filter{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Subscribe err = %v, want ErrClosed", err)
	}
}

func TestStreamReplayExpandsBufferedBundles(t *testing.T) {
	s := New(Scope{RequestID: "req_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	delta, err := EncodeEventBatch(EventBatch{
		TickID: 1,
		Events: []Event{
			{
				EventType: EventLLMReasoningDelta,
				From:      Source{Layer: "skill", ID: "skill_1"},
				Status:    StatusRunning,
				Scope:     Scope{NodeID: "node_1"},
				Delta:     json.RawMessage(`"think"`),
			},
			{
				EventType: EventLLMOutputDelta,
				From:      Source{Layer: "skill", ID: "skill_1"},
				Status:    StatusRunning,
				Scope:     Scope{NodeID: "node_1"},
				Delta:     json.RawMessage(`"answer"`),
			},
		},
	})
	if err != nil {
		t.Fatalf("EncodeEventBatch: %v", err)
	}
	if err := s.Emit(t.Context(), Event{
		EventType: EventMergeBundle,
		From:      Source{Layer: "hub"},
		Status:    StatusCompleted,
		Delta:     delta,
	}); err != nil {
		t.Fatalf("Emit bundle: %v", err)
	}

	raw, err := s.Replay(Filter{}, WithReplayFrom(1), WithRawStream())
	if err != nil {
		t.Fatalf("Replay raw: %v", err)
	}
	if len(raw) != 1 || raw[0].EventType != EventMergeBundle {
		t.Fatalf("raw replay = %+v, want one bundle event", raw)
	}

	expanded, err := s.Replay(Filter{EventTypes: []string{EventLLMOutputDelta}}, WithReplayFrom(1))
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(expanded) != 1 {
		t.Fatalf("expanded replay = %d events, want 1", len(expanded))
	}
	if expanded[0].EventType != EventLLMOutputDelta || expanded[0].Scope.RequestID != "req_1" || expanded[0].Scope.NodeID != "node_1" {
		t.Fatalf("expanded replay event = %+v, want output with merged request/node scope", expanded[0])
	}
}

func makeBundleEvent(t *testing.T, tickID uint64, nested ...Event) Event {
	t.Helper()
	delta, err := EncodeEventBatch(EventBatch{TickID: tickID, Events: nested})
	if err != nil {
		t.Fatalf("makeBundleEvent: %v", err)
	}
	return Event{
		EventType: EventMergeBundle,
		From:      Source{Layer: "hub"},
		Status:    StatusCompleted,
		Delta:     delta,
	}
}

func TestSubscribeExpandsSingleEventBatch(t *testing.T) {
	s := New(Scope{RequestID: "req_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	sub, err := s.Subscribe(Filter{}, WithNoReplay())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	nested := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "skill", ID: "sk_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"answer"`),
	}
	if err := s.Emit(t.Context(), makeBundleEvent(t, 1, nested)); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	got := receiveEvent(t, sub.Events())
	if got.EventType != EventLLMOutputDelta {
		t.Errorf("event type = %q, want %q", got.EventType, EventLLMOutputDelta)
	}
	if string(got.Delta) != `"answer"` {
		t.Errorf("delta = %s, want \"answer\"", got.Delta)
	}
	assertNoEvent(t, sub.Events())
}

func TestSubscribeExpandsMultiEventBatch(t *testing.T) {
	s := New(Scope{RequestID: "req_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	sub, err := s.Subscribe(Filter{}, WithNoReplay())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	n1 := Event{EventType: EventLLMReasoningDelta, From: Source{Layer: "skill"}, Status: StatusRunning, Delta: json.RawMessage(`"think"`)}
	n2 := Event{EventType: EventLLMOutputDelta, From: Source{Layer: "skill"}, Status: StatusRunning, Delta: json.RawMessage(`"answer"`)}
	if err := s.Emit(t.Context(), makeBundleEvent(t, 2, n1, n2)); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	first := receiveEvent(t, sub.Events())
	second := receiveEvent(t, sub.Events())
	if first.EventType != EventLLMReasoningDelta {
		t.Errorf("first type = %q, want reasoning", first.EventType)
	}
	if second.EventType != EventLLMOutputDelta {
		t.Errorf("second type = %q, want output", second.EventType)
	}
	assertNoEvent(t, sub.Events())
}

func TestSubscribeFilterSelectsMatchingNestedEvents(t *testing.T) {
	s := New(Scope{RequestID: "req_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	filter := Filter{EventTypes: []string{EventLLMOutputDelta}}
	sub, err := s.Subscribe(filter, WithNoReplay())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	n1 := Event{EventType: EventLLMReasoningDelta, From: Source{Layer: "skill"}, Status: StatusRunning, Delta: json.RawMessage(`"think"`)}
	n2 := Event{EventType: EventLLMOutputDelta, From: Source{Layer: "skill"}, Status: StatusRunning, Delta: json.RawMessage(`"answer"`)}
	n3 := Event{EventType: EventStatusProgress, From: Source{Layer: "skill"}, Status: StatusRunning, Delta: json.RawMessage(`"progress"`)}
	if err := s.Emit(t.Context(), makeBundleEvent(t, 3, n1, n2, n3)); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	got := receiveEvent(t, sub.Events())
	if got.EventType != EventLLMOutputDelta {
		t.Errorf("event type = %q, want output", got.EventType)
	}
	assertNoEvent(t, sub.Events())
}

func TestSubscribePassesThroughDirectEvents(t *testing.T) {
	s := New(Scope{RequestID: "req_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	sub, err := s.Subscribe(Filter{Prefixes: []string{"llm."}}, WithNoReplay())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	if err := s.Emit(t.Context(), Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "orchestrator"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"skip"`),
	}); err != nil {
		t.Fatalf("Emit status: %v", err)
	}
	if err := s.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "skill"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"answer"`),
	}); err != nil {
		t.Fatalf("Emit llm: %v", err)
	}

	got := receiveEvent(t, sub.Events())
	if got.EventType != EventLLMOutputDelta {
		t.Errorf("event type = %q, want output", got.EventType)
	}
	assertNoEvent(t, sub.Events())
}

func TestSubscribeReplayExpandsBundlesFromBuffer(t *testing.T) {
	s := New(Scope{RequestID: "req_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	n1 := Event{EventType: EventLLMReasoningDelta, From: Source{Layer: "skill"}, Status: StatusRunning, Scope: Scope{NodeID: "nd_1"}, Delta: json.RawMessage(`"think"`)}
	n2 := Event{EventType: EventLLMOutputDelta, From: Source{Layer: "skill"}, Status: StatusRunning, Scope: Scope{NodeID: "nd_1"}, Delta: json.RawMessage(`"answer"`)}
	if err := s.Emit(t.Context(), makeBundleEvent(t, 1, n1, n2)); err != nil {
		t.Fatalf("Emit bundle: %v", err)
	}

	sub, err := s.Subscribe(Filter{EventTypes: []string{EventLLMOutputDelta}}, WithReplayFrom(1))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	got := receiveEvent(t, sub.Events())
	if got.EventType != EventLLMOutputDelta {
		t.Errorf("event type = %q, want output", got.EventType)
	}
	// Scope from outer bundle event is merged onto nested event.
	if got.Scope.RequestID != "req_1" {
		t.Errorf("Scope.RequestID = %q, want req_1", got.Scope.RequestID)
	}
	if got.Scope.NodeID != "nd_1" {
		t.Errorf("Scope.NodeID = %q, want nd_1", got.Scope.NodeID)
	}
	assertNoEvent(t, sub.Events())
}

func TestSubscribeReplayOrderMatchesLiveSubscription(t *testing.T) {
	s := New(Scope{RequestID: "req_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	// Emit a direct event then a bundle then another direct event.
	if err := s.Emit(t.Context(), Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "orchestrator"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"start"`),
	}); err != nil {
		t.Fatalf("Emit direct 1: %v", err)
	}
	n1 := Event{EventType: EventLLMReasoningDelta, From: Source{Layer: "skill"}, Status: StatusRunning, Delta: json.RawMessage(`"think"`)}
	n2 := Event{EventType: EventLLMOutputDelta, From: Source{Layer: "skill"}, Status: StatusRunning, Delta: json.RawMessage(`"answer"`)}
	if err := s.Emit(t.Context(), makeBundleEvent(t, 1, n1, n2)); err != nil {
		t.Fatalf("Emit bundle: %v", err)
	}
	if err := s.Emit(t.Context(), Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "orchestrator"},
		Status:    StatusCompleted,
		Delta:     json.RawMessage(`"done"`),
	}); err != nil {
		t.Fatalf("Emit direct 2: %v", err)
	}

	sub, err := s.Subscribe(Filter{}, WithReplayFrom(1))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	wantTypes := []string{EventStatusProgress, EventLLMReasoningDelta, EventLLMOutputDelta, EventStatusProgress}
	for i, want := range wantTypes {
		got := receiveEvent(t, sub.Events())
		if got.EventType != want {
			t.Errorf("event[%d] type = %q, want %q", i, got.EventType, want)
		}
	}
	assertNoEvent(t, sub.Events())
}

func TestWithRawStreamSubscriberSeesBundleFrame(t *testing.T) {
	s := New(Scope{RequestID: "req_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	rawSub, err := s.Subscribe(Filter{}, WithNoReplay(), WithRawStream())
	if err != nil {
		t.Fatalf("Subscribe raw: %v", err)
	}
	defer rawSub.Cancel()

	n1 := Event{EventType: EventLLMOutputDelta, From: Source{Layer: "skill"}, Status: StatusRunning, Delta: json.RawMessage(`"out"`)}
	if err := s.Emit(t.Context(), makeBundleEvent(t, 1, n1)); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	got := receiveEvent(t, rawSub.Events())
	if got.EventType != EventMergeBundle {
		t.Errorf("raw sub event type = %q, want %q", got.EventType, EventMergeBundle)
	}
}

func TestSubscribeRejectsClosedStream(t *testing.T) {
	s := New(Scope{}, DefaultConfig())
	s.Close()

	if _, err := s.Subscribe(Filter{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Subscribe err = %v, want ErrClosed", err)
	}
}

func TestSubscribeReplayDropsWhenBufferFull(t *testing.T) {
	// Verify that replay events beyond the channel buffer are dropped under the
	// default OverflowDropNewest policy rather than causing OOM-scale allocations.
	s := New(Scope{RequestID: "req_1"}, DefaultConfig())
	t.Cleanup(s.Close)

	// Emit a bundle with 4 nested events.
	var nested []Event
	for i := range 4 {
		nested = append(nested, Event{
			EventType: EventLLMOutputDelta,
			From:      Source{Layer: "skill"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(`"out"`),
			Scope:     Scope{NodeID: fmt.Sprintf("nd_%d", i)},
		})
	}
	if err := s.Emit(t.Context(), makeBundleEvent(t, 1, nested...)); err != nil {
		t.Fatalf("Emit bundle: %v", err)
	}

	// Buffer is intentionally smaller than the 4 replay events.
	sub, err := s.Subscribe(Filter{}, WithReplayFrom(1), WithSubscriberBuffer(2))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	// Only the first 2 replay events fit; the rest are silently dropped.
	receiveEvent(t, sub.Events())
	receiveEvent(t, sub.Events())
	assertNoEvent(t, sub.Events())
}

func receiveEvent(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-ch:
		if !ok {
			t.Fatal("event channel closed")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func assertNoEvent(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case event, ok := <-ch:
		t.Fatalf("unexpected event ok=%v event=%+v", ok, event)
	case <-time.After(20 * time.Millisecond):
	}
}

func assertClosed(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("event channel still open")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel close")
	}
}
