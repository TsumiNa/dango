package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

func TestJSONEventLogStoreLoadEventsStreamsSequentialJSON(t *testing.T) {
	store, err := NewJSONEventLogStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONEventLogStore: %v", err)
	}
	ctx := context.Background()
	for seq := uint64(1); seq <= 3; seq++ {
		if err := store.AppendEvent(ctx, testJSONEvent("req_streaming", seq)); err != nil {
			t.Fatalf("AppendEvent(%d): %v", seq, err)
		}
	}
	events, err := store.LoadEvents(ctx, streampkg.Scope{RequestID: "req_streaming"}, 2, streampkg.Filter{EventTypes: []string{streampkg.EventStatusProgress}})
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].SequenceNumber != 2 || events[1].SequenceNumber != 3 {
		t.Fatalf("sequence numbers = [%d %d], want [2 3]", events[0].SequenceNumber, events[1].SequenceNumber)
	}
	if len(store.locks.locks) != defaultStripedStoreLockCount {
		t.Fatalf("lock stripe count = %d, want %d", len(store.locks.locks), defaultStripedStoreLockCount)
	}
}

func testJSONEvent(requestID string, sequence uint64) streampkg.Event {
	delta, err := json.Marshal(map[string]any{"sequence": sequence})
	if err != nil {
		panic(err)
	}
	return streampkg.Event{
		EventType:      streampkg.EventStatusProgress,
		From:           streampkg.Source{Layer: "orchestrator", ID: "orchestrator"},
		SequenceNumber: sequence,
		LogicalTime:    sequence,
		Status:         streampkg.StatusRunning,
		Delta:          delta,
		Timestamp:      time.Unix(1_700_000_000+int64(sequence), 0).UTC(),
		Scope:          streampkg.Scope{RequestID: requestID},
	}
}
