package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	streampkg "github.com/tsumina/dango/stream"
)

func TestStreamStore_AppendAndLoadEvents(t *testing.T) {
	store, cleanup := mustPostgresStore(t)
	defer cleanup()

	eventStore := NewStreamStore(store)
	requestID := fmt.Sprintf("req_postgres_stream_%d", time.Now().UnixNano())
	event := streampkg.Event{
		EventType:      streampkg.EventStatusProgress,
		From:           streampkg.Source{Layer: "orchestrator", ID: "or_postgres"},
		Status:         streampkg.StatusRunning,
		SequenceNumber: 1,
		LogicalTime:    1,
		Timestamp:      time.Now().UTC(),
		Scope:          streampkg.Scope{RequestID: requestID},
		Delta:          json.RawMessage(`{"message":"ok"}`),
	}
	if err := eventStore.AppendEvent(context.Background(), event); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	loaded, err := eventStore.LoadEvents(context.Background(), streampkg.Scope{RequestID: requestID}, 1, streampkg.Filter{})
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("len(loaded) = %d, want 1", len(loaded))
	}
	if loaded[0].EventType != event.EventType || loaded[0].SequenceNumber != event.SequenceNumber {
		t.Fatalf("loaded[0] = (%s,%d), want (%s,%d)", loaded[0].EventType, loaded[0].SequenceNumber, event.EventType, event.SequenceNumber)
	}
}

func TestStreamStore_RejectsDuplicateRequestSequence(t *testing.T) {
	store, cleanup := mustPostgresStore(t)
	defer cleanup()

	eventStore := NewStreamStore(store)
	requestID := fmt.Sprintf("req_postgres_stream_dup_%d", time.Now().UnixNano())
	event := streampkg.Event{
		EventType:      streampkg.EventStatusProgress,
		From:           streampkg.Source{Layer: "orchestrator", ID: "or_postgres"},
		Status:         streampkg.StatusRunning,
		SequenceNumber: 1,
		LogicalTime:    1,
		Timestamp:      time.Now().UTC(),
		Scope:          streampkg.Scope{RequestID: requestID},
		Delta:          json.RawMessage(`{"message":"ok"}`),
	}
	if err := eventStore.AppendEvent(context.Background(), event); err != nil {
		t.Fatalf("AppendEvent(first): %v", err)
	}
	if err := eventStore.AppendEvent(context.Background(), event); err == nil {
		t.Fatal("AppendEvent(second) succeeded, want duplicate key error")
	}
}
