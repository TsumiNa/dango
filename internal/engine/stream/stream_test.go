package stream

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestStreamEmitDeliversToMultipleSubscribers(t *testing.T) {
	s := New(Scope{RequestID: "req_1", RunnerID: "run_1"})
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
	s := New(Scope{RequestID: "req_1"})
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

	sub, err := s.Subscribe(Filter{Prefixes: []string{"llm."}}, WithReplayFrom(2), WithSubscriberBuffer(0))
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
	s := New(Scope{})
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
	s := New(Scope{})
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
	s := New(Scope{})
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
	s := New(Scope{})
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
	s := New(Scope{})
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

func TestStreamStoreAppendPrecedesDelivery(t *testing.T) {
	store := &recordingStore{}
	s := NewWithSetup(Scope{}, Setup{Store: store})
	t.Cleanup(s.Close)

	sub, err := s.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := s.Emit(t.Context(), Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "orchestrator"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"stored"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	_ = receiveEvent(t, sub.Events())

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.events) != 1 {
		t.Fatalf("store appended %d events, want 1", len(store.events))
	}
	if store.events[0].SequenceNumber != 1 {
		t.Fatalf("stored sequence = %d, want 1", store.events[0].SequenceNumber)
	}
}

func TestStreamSubscribeLoadsReplayFromStoreBeyondBuffer(t *testing.T) {
	store := &recordingStore{}
	s := NewWithSetup(Scope{RequestID: "req_store"}, Setup{Store: store, BufferLimit: 1})
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

	sub, err := s.Subscribe(Filter{Prefixes: []string{"llm."}}, WithReplayFrom(2), WithSubscriberBuffer(0))
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

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.loadCalls) != 1 {
		t.Fatalf("store load calls = %d, want 1", len(store.loadCalls))
	}
	if store.loadCalls[0].from != 2 {
		t.Fatalf("store load from = %d, want 2", store.loadCalls[0].from)
	}
	if store.loadCalls[0].scope.RequestID != "req_store" {
		t.Fatalf("store load scope = %+v, want request scope", store.loadCalls[0].scope)
	}
}

func TestStreamSubscribeReplayLastLoadsFromStoreWhenBufferDisabled(t *testing.T) {
	store := &recordingStore{}
	s := NewWithSetup(Scope{RunnerID: "run_store"}, Setup{Store: store, DisableBuffer: true})
	t.Cleanup(s.Close)

	for _, delta := range []string{`"one"`, `"two"`, `"three"`} {
		if err := s.Emit(t.Context(), Event{
			EventType: EventStatusProgress,
			From:      Source{Layer: "runner"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(delta),
		}); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}

	sub, err := s.Subscribe(Filter{}, WithReplayLast(2), WithSubscriberBuffer(0))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	first := receiveEvent(t, sub.Events())
	second := receiveEvent(t, sub.Events())
	if first.SequenceNumber != 2 || second.SequenceNumber != 3 {
		t.Fatalf("replay sequences = [%d %d], want [2 3]", first.SequenceNumber, second.SequenceNumber)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.loadCalls) != 1 {
		t.Fatalf("store load calls = %d, want 1", len(store.loadCalls))
	}
	if store.loadCalls[0].from != 2 {
		t.Fatalf("store load from = %d, want 2", store.loadCalls[0].from)
	}
}

func TestStreamSubscribeReturnsStoreLoadError(t *testing.T) {
	store := &recordingStore{loadErr: errors.New("boom")}
	s := NewWithSetup(Scope{}, Setup{Store: store, DisableBuffer: true})
	t.Cleanup(s.Close)

	if err := s.Emit(t.Context(), Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "runner"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"stored"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	if _, err := s.Subscribe(Filter{}, WithReplayLast(1)); !errors.Is(err, store.loadErr) {
		t.Fatalf("Subscribe err = %v, want load error", err)
	}
}

type recordingStore struct {
	mu        sync.Mutex
	events    []Event
	loadCalls []recordingLoadCall
	loadErr   error
}

type recordingLoadCall struct {
	scope  Scope
	from   uint64
	filter Filter
}

func (s *recordingStore) Append(_ context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *recordingStore) Load(_ context.Context, scope Scope, from uint64, filter Filter) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadCalls = append(s.loadCalls, recordingLoadCall{scope: scope, from: from, filter: filter})
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	var replay []Event
	for _, event := range s.events {
		if event.SequenceNumber < from {
			continue
		}
		if scope != (Scope{}) && event.Scope != scope {
			continue
		}
		if filter.Match(event) {
			replay = append(replay, event)
		}
	}
	return replay, nil
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
