package stream

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestEventPrepareJSONRoundTripIncludesRequiredFields(t *testing.T) {
	now := time.Date(2026, 5, 2, 1, 2, 3, 0, time.UTC)
	event, err := (Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "conversation", ID: "conv_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`{"text":"hello"}`),
	}).prepare(Scope{RequestID: "req_1", RunnerID: "run_1"}, 7, 42, func() time.Time {
		return now
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	b, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Event
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.EventType != EventLLMOutputDelta {
		t.Errorf("event_type = %q, want %q", decoded.EventType, EventLLMOutputDelta)
	}
	if decoded.From.Layer != "conversation" {
		t.Errorf("from.layer = %q, want conversation", decoded.From.Layer)
	}
	if decoded.SequenceNumber != 7 {
		t.Errorf("sequence_number = %d, want 7", decoded.SequenceNumber)
	}
	if decoded.Status != StatusRunning {
		t.Errorf("status = %q, want %q", decoded.Status, StatusRunning)
	}
	if !json.Valid(decoded.Delta) {
		t.Fatalf("delta is not valid JSON: %s", decoded.Delta)
	}
	if decoded.Scope.RequestID != "req_1" || decoded.Scope.RunnerID != "run_1" {
		t.Errorf("scope = %+v, want request and runner IDs", decoded.Scope)
	}
	if decoded.LogicalTime != 42 {
		t.Errorf("logical_time = %d, want 42", decoded.LogicalTime)
	}
	if !decoded.Timestamp.Equal(now) {
		t.Errorf("timestamp = %s, want %s", decoded.Timestamp, now)
	}
}

func TestEventPrepareRejectsInvalidEvents(t *testing.T) {
	tests := []struct {
		name  string
		event Event
	}{
		{
			name: "missing event type",
			event: Event{
				From:   Source{Layer: "orchestrator"},
				Status: StatusRunning,
				Delta:  json.RawMessage(`"ok"`),
			},
		},
		{
			name: "missing source layer",
			event: Event{
				EventType: EventStatusProgress,
				Status:    StatusRunning,
				Delta:     json.RawMessage(`"ok"`),
			},
		},
		{
			name: "missing status",
			event: Event{
				EventType: EventStatusProgress,
				From:      Source{Layer: "orchestrator"},
				Delta:     json.RawMessage(`"ok"`),
			},
		},
		{
			name: "invalid delta",
			event: Event{
				EventType: EventStatusProgress,
				From:      Source{Layer: "orchestrator"},
				Status:    StatusRunning,
				Delta:     json.RawMessage(`{`),
			},
		},
		{
			name: "invalid metadata",
			event: Event{
				EventType: EventStatusProgress,
				From:      Source{Layer: "orchestrator"},
				Status:    StatusRunning,
				Delta:     json.RawMessage(`"ok"`),
				Metadata:  map[string]any{"bad": make(chan int)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.event.prepare(Scope{}, 1, 1, time.Now)
			if !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("prepare err = %v, want ErrInvalidEvent", err)
			}
		})
	}
}
