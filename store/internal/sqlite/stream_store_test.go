package sqlite

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	streampkg "github.com/tsumina/dango/stream"
)

func TestStreamStoreLoadEventsFiltersAndIsolatesRequests(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "dango.db")
	dbStore, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanupStore := dbStore
	t.Cleanup(func() {
		if cleanupStore == nil {
			return
		}
		if err := cleanupStore.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	eventLog := NewStreamStore(dbStore)

	appendEvent(t, eventLog, preparedStreamEvent("req_1", 1, streampkg.EventStatusProgress, streampkg.Source{Layer: "orchestrator", ID: "or_1"}, streampkg.StatusRunning, streampkg.Scope{}, json.RawMessage(`"planning"`)))
	appendEvent(t, eventLog, preparedStreamEvent("req_1", 2, streampkg.EventLLMReasoningDelta, streampkg.Source{Layer: "skill", ID: "skill_1", ParentID: "agent_1"}, streampkg.StatusRunning, streampkg.Scope{RunnerID: "run_1", NodeID: "node_1"}, json.RawMessage(`"think"`)))
	appendEvent(t, eventLog, preparedStreamEvent("req_1", 3, streampkg.EventLLMOutputDelta, streampkg.Source{Layer: "skill", ID: "skill_1", ParentID: "agent_1"}, streampkg.StatusCompleted, streampkg.Scope{RunnerID: "run_1", NodeID: "node_1"}, json.RawMessage(`"answer"`)))
	appendEvent(t, eventLog, preparedStreamEvent("req_2", 1, streampkg.EventLLMOutputDelta, streampkg.Source{Layer: "skill", ID: "skill_2", ParentID: "agent_2"}, streampkg.StatusCompleted, streampkg.Scope{RunnerID: "run_2", NodeID: "node_2"}, json.RawMessage(`"other request"`)))

	if err := dbStore.Close(); err != nil {
		t.Fatalf("Close before reopen: %v", err)
	}
	cleanupStore = nil
	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(reopen): %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Fatalf("Close(reopen): %v", err)
		}
	})
	reopenedLog := NewStreamStore(reopened)

	replay, err := reopenedLog.LoadEvents(t.Context(), streampkg.Scope{RequestID: "req_1"}, 2, streampkg.Filter{
		Prefixes: []string{"llm."},
		Sources: []streampkg.SourceSelector{{
			Layer:    "skill",
			ID:       "skill_1",
			ParentID: "agent_1",
		}},
		Scope: streampkg.Scope{RunnerID: "run_1", NodeID: "node_1"},
	})
	if err != nil {
		t.Fatalf("LoadEvents filtered: %v", err)
	}
	if len(replay) != 2 {
		t.Fatalf("len(replay) = %d, want 2", len(replay))
	}
	if replay[0].SequenceNumber != 2 || replay[0].EventType != streampkg.EventLLMReasoningDelta {
		t.Fatalf("first replay = %+v, want seq 2 reasoning", replay[0])
	}
	if replay[1].SequenceNumber != 3 || replay[1].EventType != streampkg.EventLLMOutputDelta {
		t.Fatalf("second replay = %+v, want seq 3 output", replay[1])
	}
	for _, event := range replay {
		if event.Scope.RequestID != "req_1" || event.Scope.RunnerID != "run_1" || event.Scope.NodeID != "node_1" {
			t.Fatalf("event scope = %+v, want request/run/node req_1/run_1/node_1", event.Scope)
		}
	}

	outputOnly, err := reopenedLog.LoadEvents(t.Context(), streampkg.Scope{RequestID: "req_1"}, 1, streampkg.Filter{
		EventTypes: []string{streampkg.EventLLMOutputDelta},
		Scope:      streampkg.Scope{RunnerID: "run_1"},
	})
	if err != nil {
		t.Fatalf("LoadEvents exact event type: %v", err)
	}
	if len(outputOnly) != 1 {
		t.Fatalf("len(outputOnly) = %d, want 1", len(outputOnly))
	}
	if outputOnly[0].Scope.RequestID != "req_1" || string(outputOnly[0].Delta) != `"answer"` {
		t.Fatalf("output replay = %+v, want req_1 answer", outputOnly[0])
	}
}

func TestStreamStorePersistsRawMergeBundles(t *testing.T) {
	t.Parallel()

	dbStore, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := dbStore.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	eventLog := NewStreamStore(dbStore)

	batch, err := streampkg.EncodeEventBatch(streampkg.EventBatch{
		TickID: 7,
		Events: []streampkg.Event{
			{
				EventType: streampkg.EventLLMReasoningDelta,
				From:      streampkg.Source{Layer: "skill", ID: "skill_1"},
				Status:    streampkg.StatusRunning,
				Scope:     streampkg.Scope{RunnerID: "run_bundle", NodeID: "node_1"},
				Delta:     json.RawMessage(`"first"`),
			},
			{
				EventType: streampkg.EventLLMOutputDelta,
				From:      streampkg.Source{Layer: "skill", ID: "skill_1"},
				Status:    streampkg.StatusCompleted,
				Scope:     streampkg.Scope{RunnerID: "run_bundle", NodeID: "node_1"},
				Delta:     json.RawMessage(`"second"`),
			},
		},
	})
	if err != nil {
		t.Fatalf("EncodeEventBatch: %v", err)
	}
	appendEvent(t, eventLog, preparedStreamEvent("req_bundle", 1, streampkg.EventMergeBundle, streampkg.Source{Layer: "hub", ID: "hub_1"}, streampkg.StatusCompleted, streampkg.Scope{}, batch))

	raw, err := eventLog.LoadEvents(t.Context(), streampkg.Scope{RequestID: "req_bundle"}, 1, streampkg.Filter{})
	if err != nil {
		t.Fatalf("LoadEvents raw: %v", err)
	}
	if len(raw) != 1 || raw[0].EventType != streampkg.EventMergeBundle {
		t.Fatalf("raw replay = %+v, want one merge bundle", raw)
	}
	decoded, err := streampkg.DecodeEventBatch(raw[0].Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch: %v", err)
	}
	if len(decoded.Events) != 2 || decoded.Events[0].EventType != streampkg.EventLLMReasoningDelta || decoded.Events[1].EventType != streampkg.EventLLMOutputDelta {
		t.Fatalf("bundle events = %+v, want reasoning then output", decoded.Events)
	}
}

func TestStreamStoreLoadEventsFromBeyondInt64ReturnsNoRows(t *testing.T) {
	t.Parallel()

	dbStore, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := dbStore.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	eventLog := NewStreamStore(dbStore)
	appendEvent(t, eventLog, preparedStreamEvent("req_big_from", 1, streampkg.EventStatusProgress, streampkg.Source{Layer: "orchestrator", ID: "or_1"}, streampkg.StatusRunning, streampkg.Scope{}, json.RawMessage(`"planning"`)))

	replay, err := eventLog.LoadEvents(t.Context(), streampkg.Scope{RequestID: "req_big_from"}, math.MaxUint64, streampkg.Filter{})
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(replay) != 0 {
		t.Fatalf("len(replay) = %d, want 0", len(replay))
	}
}

func TestStreamStoreRejectsEventsWithoutRequestID(t *testing.T) {
	t.Parallel()

	dbStore, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := dbStore.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	err = NewStreamStore(dbStore).AppendEvent(context.Background(), streampkg.Event{
		EventType:      streampkg.EventStatusProgress,
		From:           streampkg.Source{Layer: "runner", ID: "run_1"},
		SequenceNumber: 1,
		LogicalTime:    1,
		Status:         streampkg.StatusRunning,
		Timestamp:      time.Now().UTC(),
		Delta:          json.RawMessage(`"missing request"`),
	})
	if err == nil {
		t.Fatal("AppendEvent() error = nil, want request_id validation error")
	}
	if !strings.Contains(err.Error(), "request_id") {
		t.Fatalf("AppendEvent() error = %v, want request_id in message", err)
	}
}

func appendEvent(t *testing.T, eventLog *StreamStore, event streampkg.Event) {
	t.Helper()
	if err := eventLog.AppendEvent(t.Context(), event); err != nil {
		t.Fatalf("AppendEvent(%s): %v", event.EventType, err)
	}
}

func preparedStreamEvent(requestID string, seq uint64, eventType string, source streampkg.Source, status string, scope streampkg.Scope, delta json.RawMessage) streampkg.Event {
	scope.RequestID = requestID
	return streampkg.Event{
		EventType:      eventType,
		From:           source,
		Status:         status,
		Scope:          scope,
		SequenceNumber: seq,
		LogicalTime:    seq,
		Timestamp:      time.Unix(1_700_000_000+int64(seq), 0).UTC(),
		Delta:          delta,
	}
}
