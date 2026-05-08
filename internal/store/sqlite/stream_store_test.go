package sqlite

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

func TestStreamStoreReplayFiltersAndIsolatesRequests(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "dango.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanupStore := store

	streamStore := NewStreamStore(store)
	requestStream := streampkg.New(
		streampkg.Scope{RequestID: "req_1"},
		streampkg.Config{DisableBuffer: true},
		streampkg.WithStore(streamStore),
	)
	otherRequestStream := streampkg.New(
		streampkg.Scope{RequestID: "req_2"},
		streampkg.Config{DisableBuffer: true},
		streampkg.WithStore(streamStore),
	)
	t.Cleanup(requestStream.Close)
	t.Cleanup(otherRequestStream.Close)
	t.Cleanup(func() {
		if cleanupStore == nil {
			return
		}
		if err := cleanupStore.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	emit := func(t *testing.T, s *streampkg.Stream, event streampkg.Event) {
		t.Helper()
		if err := s.Emit(t.Context(), event); err != nil {
			t.Fatalf("Emit(%s): %v", event.EventType, err)
		}
	}

	emit(t, requestStream, streampkg.Event{
		EventType: streampkg.EventStatusProgress,
		From:      streampkg.Source{Layer: "orchestrator", ID: "or_1"},
		Status:    streampkg.StatusRunning,
		Delta:     json.RawMessage(`"planning"`),
	})
	emit(t, requestStream, streampkg.Event{
		EventType: streampkg.EventLLMReasoningDelta,
		From:      streampkg.Source{Layer: "skill", ID: "skill_1", ParentID: "executor_1"},
		Status:    streampkg.StatusRunning,
		Scope:     streampkg.Scope{RunnerID: "run_1", NodeID: "node_1"},
		Delta:     json.RawMessage(`"think"`),
	})
	emit(t, requestStream, streampkg.Event{
		EventType: streampkg.EventLLMOutputDelta,
		From:      streampkg.Source{Layer: "skill", ID: "skill_1", ParentID: "executor_1"},
		Status:    streampkg.StatusCompleted,
		Scope:     streampkg.Scope{RunnerID: "run_1", NodeID: "node_1"},
		Delta:     json.RawMessage(`"answer"`),
	})
	emit(t, otherRequestStream, streampkg.Event{
		EventType: streampkg.EventLLMOutputDelta,
		From:      streampkg.Source{Layer: "skill", ID: "skill_2", ParentID: "executor_2"},
		Status:    streampkg.StatusCompleted,
		Scope:     streampkg.Scope{RunnerID: "run_2", NodeID: "node_2"},
		Delta:     json.RawMessage(`"other request"`),
	})

	requestStream.Close()
	otherRequestStream.Close()
	if err := store.Close(); err != nil {
		t.Fatalf("Close before reopen: %v", err)
	}
	cleanupStore = nil
	store = nil

	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(reopen): %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Fatalf("Close(reopen): %v", err)
		}
	})

	replayStream := streampkg.New(
		streampkg.Scope{RequestID: "req_1"},
		streampkg.Config{DisableBuffer: true},
		streampkg.WithStore(NewStreamStore(reopened)),
	)
	t.Cleanup(replayStream.Close)

	replay, err := replayStream.Replay(streampkg.Filter{
		Prefixes: []string{"llm."},
		Sources: []streampkg.SourceSelector{{
			Layer:    "skill",
			ID:       "skill_1",
			ParentID: "executor_1",
		}},
		Scope: streampkg.Scope{RunnerID: "run_1", NodeID: "node_1"},
	}, streampkg.WithReplayFrom(2))
	if err != nil {
		t.Fatalf("Replay filtered: %v", err)
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

	outputOnly, err := replayStream.Replay(streampkg.Filter{
		EventTypes: []string{streampkg.EventLLMOutputDelta},
		Scope:      streampkg.Scope{RunnerID: "run_1"},
	}, streampkg.WithReplayFrom(1))
	if err != nil {
		t.Fatalf("Replay exact event type: %v", err)
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

	dbPath := filepath.Join(t.TempDir(), "dango.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	requestStream := streampkg.New(
		streampkg.Scope{RequestID: "req_bundle"},
		streampkg.Config{DisableBuffer: true},
		streampkg.WithStore(NewStreamStore(store)),
	)
	t.Cleanup(requestStream.Close)

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
	if err := requestStream.Emit(t.Context(), streampkg.Event{
		EventType: streampkg.EventMergeBundle,
		From:      streampkg.Source{Layer: "hub", ID: "hub_1"},
		Status:    streampkg.StatusCompleted,
		Delta:     batch,
	}); err != nil {
		t.Fatalf("Emit bundle: %v", err)
	}

	replayStream := streampkg.New(
		streampkg.Scope{RequestID: "req_bundle"},
		streampkg.Config{DisableBuffer: true},
		streampkg.WithStore(NewStreamStore(store)),
	)
	t.Cleanup(replayStream.Close)

	raw, err := replayStream.Replay(streampkg.Filter{}, streampkg.WithReplayFrom(1), streampkg.WithRawStream())
	if err != nil {
		t.Fatalf("Replay raw: %v", err)
	}
	if len(raw) != 1 || raw[0].EventType != streampkg.EventMergeBundle {
		t.Fatalf("raw replay = %+v, want one merge bundle", raw)
	}

	expanded, err := replayStream.Replay(streampkg.Filter{Prefixes: []string{"llm."}}, streampkg.WithReplayFrom(1))
	if err != nil {
		t.Fatalf("Replay expanded: %v", err)
	}
	if len(expanded) != 2 {
		t.Fatalf("len(expanded) = %d, want 2", len(expanded))
	}
	if expanded[0].Scope.RequestID != "req_bundle" || expanded[1].Scope.RequestID != "req_bundle" {
		t.Fatalf("expanded request ids = [%q %q], want req_bundle", expanded[0].Scope.RequestID, expanded[1].Scope.RequestID)
	}
}

func TestStreamStoreReplayFromBeyondInt64ReturnsNoRows(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "dango.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	requestStream := streampkg.New(
		streampkg.Scope{RequestID: "req_big_from"},
		streampkg.Config{DisableBuffer: true},
		streampkg.WithStore(NewStreamStore(store)),
	)
	t.Cleanup(requestStream.Close)

	if err := requestStream.Emit(t.Context(), streampkg.Event{
		EventType: streampkg.EventStatusProgress,
		From:      streampkg.Source{Layer: "orchestrator", ID: "or_1"},
		Status:    streampkg.StatusRunning,
		Delta:     json.RawMessage(`"planning"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	replayStream := streampkg.New(
		streampkg.Scope{RequestID: "req_big_from"},
		streampkg.Config{DisableBuffer: true},
		streampkg.WithStore(NewStreamStore(store)),
	)
	t.Cleanup(replayStream.Close)

	replay, err := replayStream.Replay(streampkg.Filter{}, streampkg.WithReplayFrom(math.MaxUint64))
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replay) != 0 {
		t.Fatalf("len(replay) = %d, want 0", len(replay))
	}
}

func TestStreamStoreRejectsEventsWithoutRequestID(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	err = NewStreamStore(store).Append(context.Background(), streampkg.Event{
		EventType:      streampkg.EventStatusProgress,
		From:           streampkg.Source{Layer: "runner", ID: "run_1"},
		SequenceNumber: 1,
		LogicalTime:    1,
		Status:         streampkg.StatusRunning,
		Timestamp:      time.Now().UTC(),
		Delta:          json.RawMessage(`"missing request"`),
	})
	if err == nil {
		t.Fatal("Append() error = nil, want request_id validation error")
	}
	if !strings.Contains(err.Error(), "request_id") {
		t.Fatalf("Append() error = %v, want request_id in message", err)
	}
}
