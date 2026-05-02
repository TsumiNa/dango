package stream

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestStreamMergeFromCombinesMultipleUpstreams(t *testing.T) {
	parent := New(Scope{RequestID: "req_1"})
	childA := New(Scope{NodeID: "node_a"})
	childB := New(Scope{NodeID: "node_b"})
	t.Cleanup(parent.Close)
	t.Cleanup(childA.Close)
	t.Cleanup(childB.Close)

	sub, err := parent.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe parent: %v", err)
	}
	mergeA, err := parent.MergeFrom(t.Context(), childA, Filter{})
	if err != nil {
		t.Fatalf("MergeFrom childA: %v", err)
	}
	mergeB, err := parent.MergeFrom(t.Context(), childB, Filter{})
	if err != nil {
		t.Fatalf("MergeFrom childB: %v", err)
	}
	defer mergeA.Stop()
	defer mergeB.Stop()

	if err := childA.Emit(t.Context(), Event{
		EventType: EventExecutorExecuteStarted,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`{"stage":"execute"}`),
	}); err != nil {
		t.Fatalf("Emit childA: %v", err)
	}
	if err := childB.Emit(t.Context(), Event{
		EventType: EventExecutorExecuteCompleted,
		From:      Source{Layer: "executor", ID: "node_b"},
		Status:    StatusCompleted,
		Delta:     json.RawMessage(`{"stage":"execute"}`),
	}); err != nil {
		t.Fatalf("Emit childB: %v", err)
	}

	first, ok, err := sub.Next(t.Context())
	if err != nil || !ok {
		t.Fatalf("Next first = ok %v err %v", ok, err)
	}
	second, ok, err := sub.Next(t.Context())
	if err != nil || !ok {
		t.Fatalf("Next second = ok %v err %v", ok, err)
	}
	if first.SequenceNumber != 1 || second.SequenceNumber != 2 {
		t.Fatalf("downstream sequences = %d,%d want 1,2", first.SequenceNumber, second.SequenceNumber)
	}
	byNode := map[string]Event{
		first.Scope.NodeID:  first,
		second.Scope.NodeID: second,
	}
	for _, nodeID := range []string{"node_a", "node_b"} {
		event := byNode[nodeID]
		if event.Scope.RequestID != "req_1" || event.Scope.NodeID != nodeID {
			t.Fatalf("%s scope = %+v, want request parent and child node", nodeID, event.Scope)
		}
		if event.Metadata["upstream_sequence_number"] != uint64(1) {
			t.Fatalf("%s upstream sequence metadata = %#v, want 1", nodeID, event.Metadata["upstream_sequence_number"])
		}
	}
}

func TestStreamMergeFromFiltersAndReplaysUpstream(t *testing.T) {
	parent := New(Scope{})
	child := New(Scope{})
	t.Cleanup(parent.Close)
	t.Cleanup(child.Close)

	if err := child.Emit(t.Context(), Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "executor"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hidden"`),
	}); err != nil {
		t.Fatalf("Emit status: %v", err)
	}
	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "skill"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"visible"`),
	}); err != nil {
		t.Fatalf("Emit llm: %v", err)
	}

	sub, err := parent.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe parent: %v", err)
	}
	merge, err := parent.MergeFrom(t.Context(), child, Filter{Prefixes: []string{"llm."}}, WithReplayFrom(1))
	if err != nil {
		t.Fatalf("MergeFrom: %v", err)
	}
	defer merge.Stop()

	event, ok, err := sub.Next(t.Context())
	if err != nil || !ok {
		t.Fatalf("Next replay = ok %v err %v", ok, err)
	}
	if event.EventType != EventLLMOutputDelta {
		t.Fatalf("event type = %q, want %q", event.EventType, EventLLMOutputDelta)
	}
	assertNoEvent(t, sub.Events())
}

func TestStreamMergeFromRejectsInvalidSources(t *testing.T) {
	s := New(Scope{})
	t.Cleanup(s.Close)
	if _, err := s.MergeFrom(t.Context(), nil, Filter{}); !errors.Is(err, ErrInvalidMerge) {
		t.Fatalf("MergeFrom nil err = %v, want ErrInvalidMerge", err)
	}
	if _, err := s.MergeFrom(t.Context(), s, Filter{}); !errors.Is(err, ErrInvalidMerge) {
		t.Fatalf("MergeFrom self err = %v, want ErrInvalidMerge", err)
	}
}
