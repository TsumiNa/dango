package stream

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestStreamEmitsIncreasingLogicalTimes(t *testing.T) {
	ctx := context.Background()
	s := New(Scope{RequestID: "req_1"}, Config{})
	defer s.Close()

	sub, err := s.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Emit multiple events
	for i := 1; i <= 5; i++ {
		err := s.Emit(ctx, Event{
			EventType: EventStatusProgress,
			From:      Source{Layer: "test"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(`{"iteration":` + string(rune('0'+i)) + `}`),
		})
		if err != nil {
			t.Fatalf("Emit %d: %v", i, err)
		}
	}

	// Collect events from subscription
	events := make([]Event, 0, 5)
	done := make(chan struct{})
	go func() {
		for {
			ev, ok, err := sub.Next(ctx)
			if !ok || err != nil {
				close(done)
				return
			}
			events = append(events, ev)
			if len(events) == 5 {
				sub.Cancel()
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for events")
	}

	if len(events) != 5 {
		t.Fatalf("collected events = %d, want 5", len(events))
	}

	// Verify logical times are increasing
	for i, ev := range events {
		expectedLogicalTime := uint64(i + 1)
		if ev.LogicalTime != expectedLogicalTime {
			t.Errorf("event %d logical_time = %d, want %d", i, ev.LogicalTime, expectedLogicalTime)
		}
	}
}

func TestStreamLogicalTimeIndependentOfSequenceNumber(t *testing.T) {
	ctx := context.Background()
	s := New(Scope{RequestID: "req_1"}, Config{})
	defer s.Close()

	sub, err := s.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Emit events
	err = s.Emit(ctx, Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "test"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`null`),
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	err = s.Emit(ctx, Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "test"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`null`),
	})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Collect events
	events := make([]Event, 0, 2)
	done := make(chan struct{})
	go func() {
		for {
			ev, ok, err := sub.Next(ctx)
			if !ok || err != nil {
				close(done)
				return
			}
			events = append(events, ev)
			if len(events) == 2 {
				sub.Cancel()
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for events")
	}

	if len(events) != 2 {
		t.Fatalf("collected events = %d, want 2", len(events))
	}

	// Both sequence number and logical time should increase independently
	if events[0].SequenceNumber != 1 {
		t.Errorf("event 0 sequence_number = %d, want 1", events[0].SequenceNumber)
	}
	if events[0].LogicalTime != 1 {
		t.Errorf("event 0 logical_time = %d, want 1", events[0].LogicalTime)
	}

	if events[1].SequenceNumber != 2 {
		t.Errorf("event 1 sequence_number = %d, want 2", events[1].SequenceNumber)
	}
	if events[1].LogicalTime != 2 {
		t.Errorf("event 1 logical_time = %d, want 2", events[1].LogicalTime)
	}
}

func TestLogicalTimeUnmarshalsTolerateMissingField(t *testing.T) {
	// Test that existing JSON without logical_time field can still unmarshal
	oldJSON := `{
		"event_type": "status.progress",
		"from": {"layer": "test"},
		"sequence_number": 5,
		"status": "running",
		"delta": null
	}`

	var ev Event
	if err := json.Unmarshal([]byte(oldJSON), &ev); err != nil {
		t.Fatalf("unmarshal old JSON: %v", err)
	}

	if ev.EventType != EventStatusProgress {
		t.Errorf("event_type = %q, want %q", ev.EventType, EventStatusProgress)
	}
	if ev.SequenceNumber != 5 {
		t.Errorf("sequence_number = %d, want 5", ev.SequenceNumber)
	}
	if ev.LogicalTime != 0 {
		t.Errorf("logical_time = %d, want 0 (zero value)", ev.LogicalTime)
	}
}

func TestLogicalTimeIncreasesAcrossStandaloneMerges(t *testing.T) {
	ctx := context.Background()

	// Create two upstream streams
	up1 := New(Scope{RequestID: "req_1"}, Config{})
	defer up1.Close()

	up2 := New(Scope{RequestID: "req_1"}, Config{})
	defer up2.Close()

	// Merge them
	merged := New(Scope{RequestID: "req_1"}, Config{})
	defer merged.Close()

	_, err := merged.mergeFrom(ctx, up1, Filter{})
	if err != nil {
		t.Fatalf("mergeFrom up1: %v", err)
	}

	_, err = merged.mergeFrom(ctx, up2, Filter{})
	if err != nil {
		t.Fatalf("mergeFrom up2: %v", err)
	}

	// Subscribe to merged stream
	sub, err := merged.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Emit from first upstream
	err = up1.Emit(ctx, Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "test"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`{"source":"up1"}`),
	})
	if err != nil {
		t.Fatalf("Emit up1: %v", err)
	}

	// Emit from second upstream
	err = up2.Emit(ctx, Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "test"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`{"source":"up2"}`),
	})
	if err != nil {
		t.Fatalf("Emit up2: %v", err)
	}

	// Collect events from merged stream
	events := make([]Event, 0, 2)
	done := make(chan struct{})
	go func() {
		for {
			ev, ok, err := sub.Next(ctx)
			if !ok || err != nil {
				close(done)
				return
			}
			events = append(events, ev)
			if len(events) == 2 {
				sub.Cancel()
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for events")
	}

	if len(events) != 2 {
		t.Fatalf("collected events = %d, want 2", len(events))
	}

	// Each upstream has its own logical time sequence
	if events[0].LogicalTime == 0 {
		t.Errorf("event 0 logical_time = 0, should be non-zero")
	}
	if events[1].LogicalTime == 0 {
		t.Errorf("event 1 logical_time = 0, should be non-zero")
	}

	// The merged stream should have its own logical time sequence
	// (different from the upstream logical times)
	if events[0].LogicalTime != 1 {
		t.Errorf("event 0 merged logical_time = %d, want 1", events[0].LogicalTime)
	}
	if events[1].LogicalTime != 2 {
		t.Errorf("event 1 merged logical_time = %d, want 2", events[1].LogicalTime)
	}
}
