package stream

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestSubscriptionNextReturnsEventsAndClose(t *testing.T) {
	s := New(Scope{}, DefaultConfig())
	sub, err := s.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := s.Emit(t.Context(), Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "runner"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"tick"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	event, ok, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next err: %v", err)
	}
	if !ok {
		t.Fatal("Next ok=false, want event")
	}
	if event.EventType != EventStatusProgress {
		t.Fatalf("event type = %q, want %q", event.EventType, EventStatusProgress)
	}

	s.Close()
	_, ok, err = sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next after close err: %v", err)
	}
	if ok {
		t.Fatal("Next after close ok=true, want false")
	}
}

func TestSubscriptionNextReturnsContextError(t *testing.T) {
	s := New(Scope{}, DefaultConfig())
	t.Cleanup(s.Close)
	sub, err := s.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, ok, err := sub.Next(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next err = %v, want context deadline exceeded", err)
	}
	if ok {
		t.Fatal("Next ok=true, want false on context error")
	}
}
