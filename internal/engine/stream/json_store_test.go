package stream

import (
	"encoding/json"
	"os"
	"testing"
)

func TestNewJSONStoreRejectsEmptyDirectory(t *testing.T) {
	if _, err := NewJSONStore(""); err == nil {
		t.Fatal("NewJSONStore() err = nil, want error")
	}
}

func TestJSONStorePersistsReplayAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONStore(dir)
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	stream := New(Scope{RunnerID: "run_1"}, Config{DisableBuffer: true}, WithStore(store))
	t.Cleanup(stream.Close)

	emit := func(eventType string, nodeID string, delta string) {
		t.Helper()
		event := Event{
			EventType: eventType,
			From:      Source{Layer: "conversation", ID: "conv_1"},
			Status:    StatusRunning,
			Delta:     []byte(delta),
		}
		if nodeID != "" {
			event.Scope = Scope{NodeID: nodeID}
		}
		if err := stream.Emit(t.Context(), event); err != nil {
			t.Fatalf("Emit %s: %v", eventType, err)
		}
	}

	emit(EventStatusProgress, "", `"planning"`)
	emit(EventLLMReasoningDelta, "n1", `"think"`)
	emit(EventLLMOutputDelta, "n2", `"answer"`)

	reopenedStore, err := NewJSONStore(dir)
	if err != nil {
		t.Fatalf("NewJSONStore(reopen): %v", err)
	}
	replayStream := New(Scope{RunnerID: "run_1"}, Config{DisableBuffer: true}, WithStore(reopenedStore))
	t.Cleanup(replayStream.Close)

	sub, err := replayStream.Subscribe(Filter{Prefixes: []string{"llm."}}, WithReplayFrom(2), WithSubscriberBuffer(0))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	first := receiveEvent(t, sub.Events())
	second := receiveEvent(t, sub.Events())
	if first.SequenceNumber != 2 || first.EventType != EventLLMReasoningDelta || first.Scope.NodeID != "n1" {
		t.Fatalf("first replay = %+v, want seq 2 reasoning for node n1", first)
	}
	if second.SequenceNumber != 3 || second.EventType != EventLLMOutputDelta || second.Scope.NodeID != "n2" {
		t.Fatalf("second replay = %+v, want seq 3 output for node n2", second)
	}
	assertNoEvent(t, sub.Events())
}

func TestJSONStorePersistsBundleEventsWithNestedEvents(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONStore(dir)
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	stream := New(Scope{RequestID: "req_1"}, Config{DisableBuffer: true}, WithStore(store))
	t.Cleanup(stream.Close)

	delta, err := EncodeBundlePayload(BundlePayload{
		TickID: 3,
		NestedEvents: []Event{
			{
				EventType: EventLLMReasoningDelta,
				From:      Source{Layer: "skill", ID: "skill_1"},
				Status:    StatusRunning,
				Scope:     Scope{NodeID: "node_1"},
				Delta:     json.RawMessage(`"first"`),
			},
			{
				EventType: EventLLMOutputDelta,
				From:      Source{Layer: "skill", ID: "skill_1"},
				Status:    StatusCompleted,
				Scope:     Scope{NodeID: "node_1"},
				Delta:     json.RawMessage(`"second"`),
			},
		},
	})
	if err != nil {
		t.Fatalf("EncodeBundlePayload: %v", err)
	}
	if err := stream.Emit(t.Context(), Event{
		EventType: EventMergeBundle,
		From:      Source{Layer: "hub"},
		Status:    StatusCompleted,
		Delta:     delta,
	}); err != nil {
		t.Fatalf("Emit bundle: %v", err)
	}

	reopenedStore, err := NewJSONStore(dir)
	if err != nil {
		t.Fatalf("NewJSONStore(reopen): %v", err)
	}
	replayStream := New(Scope{RequestID: "req_1"}, Config{DisableBuffer: true}, WithStore(reopenedStore))
	t.Cleanup(replayStream.Close)

	raw, err := replayStream.Replay(Filter{}, WithReplayFrom(1))
	if err != nil {
		t.Fatalf("Replay raw: %v", err)
	}
	if len(raw) != 1 || raw[0].EventType != EventMergeBundle {
		t.Fatalf("raw replay = %+v, want one bundle event", raw)
	}
	bundle, err := DecodeBundlePayload(raw[0].Delta)
	if err != nil {
		t.Fatalf("DecodeBundlePayload: %v", err)
	}
	if len(bundle.NestedEvents) != 2 || bundle.NestedEvents[0].EventType != EventLLMReasoningDelta || bundle.NestedEvents[1].EventType != EventLLMOutputDelta {
		t.Fatalf("nested events = %+v, want reasoning then output", bundle.NestedEvents)
	}

	expanded, err := replayStream.ReplayExpanded(Filter{Prefixes: []string{"llm."}}, WithReplayFrom(1))
	if err != nil {
		t.Fatalf("ReplayExpanded: %v", err)
	}
	if len(expanded) != 2 || expanded[0].Delta == nil || expanded[1].Delta == nil {
		t.Fatalf("expanded replay = %+v, want two nested events", expanded)
	}
}

func TestJSONStoreLoadToleratesPartialTrailingLine(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONStore(dir)
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	stream := New(Scope{}, Config{DisableBuffer: true}, WithStore(store))
	t.Cleanup(stream.Close)

	for _, delta := range []string{`"one"`, `"two"`} {
		if err := stream.Emit(t.Context(), Event{
			EventType: EventStatusProgress,
			From:      Source{Layer: "runner"},
			Status:    StatusRunning,
			Delta:     []byte(delta),
		}); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}

	f, err := os.OpenFile(store.path(), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.WriteString(`{"event_type":"broken"`); err != nil {
		f.Close()
		t.Fatalf("WriteString: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopenedStore, err := NewJSONStore(dir)
	if err != nil {
		t.Fatalf("NewJSONStore(reopen): %v", err)
	}
	events, err := reopenedStore.Load(t.Context(), Scope{}, 1, Filter{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].SequenceNumber != 1 || events[1].SequenceNumber != 2 {
		t.Fatalf("loaded sequences = [%d %d], want [1 2]", events[0].SequenceNumber, events[1].SequenceNumber)
	}
}
