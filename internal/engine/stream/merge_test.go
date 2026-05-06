package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestStreamMergeFromCombinesMultipleUpstreams(t *testing.T) {
	parent := New(Scope{RequestID: "req_1"}, DefaultConfig())
	childA := New(Scope{NodeID: "node_a"}, DefaultConfig())
	childB := New(Scope{NodeID: "node_b"}, DefaultConfig())
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
	parent := New(Scope{}, DefaultConfig())
	child := New(Scope{}, DefaultConfig())
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
	s := New(Scope{}, DefaultConfig())
	t.Cleanup(s.Close)
	if _, err := s.MergeFrom(t.Context(), nil, Filter{}); !errors.Is(err, ErrInvalidMerge) {
		t.Fatalf("MergeFrom nil err = %v, want ErrInvalidMerge", err)
	}
	if _, err := s.MergeFrom(t.Context(), s, Filter{}); !errors.Is(err, ErrInvalidMerge) {
		t.Fatalf("MergeFrom self err = %v, want ErrInvalidMerge", err)
	}
}

func TestUpstreamIdentitySameSourceShareIdentity(t *testing.T) {
	src1 := Source{Layer: "executor", ID: "node_a"}
	src2 := Source{Layer: "executor", ID: "node_a"}
	src3 := Source{Layer: "executor", ID: "node_b"}
	src4 := Source{Layer: "skill", ID: "node_a"}

	id1 := upstreamIdentityOf(src1)
	id2 := upstreamIdentityOf(src2)
	id3 := upstreamIdentityOf(src3)
	id4 := upstreamIdentityOf(src4)

	if id1 != id2 {
		t.Fatalf("same source should produce same identity: %+v vs %+v", id1, id2)
	}
	if id1 == id3 {
		t.Fatalf("different ID should produce different identity: %+v vs %+v", id1, id3)
	}
	if id1 == id4 {
		t.Fatalf("different Layer should produce different identity: %+v vs %+v", id1, id4)
	}
}

func TestJoinKeySameEventPropertiesShareKey(t *testing.T) {
	event1 := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}
	event2 := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`" world"`),
	}
	event3 := Event{
		EventType: EventLLMReasoningDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}
	event4 := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_b"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}
	event5 := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusCompleted,
		Delta:     json.RawMessage(`"hello"`),
	}

	key1 := joinKeyOf(event1)
	key2 := joinKeyOf(event2)
	key3 := joinKeyOf(event3)
	key4 := joinKeyOf(event4)
	key5 := joinKeyOf(event5)

	if key1 != key2 {
		t.Fatalf("same upstream/type/status should share key: %+v vs %+v", key1, key2)
	}
	if key1 == key3 {
		t.Fatalf("different EventType should produce different key: %+v vs %+v", key1, key3)
	}
	if key1 == key4 {
		t.Fatalf("different upstream ID should produce different key: %+v vs %+v", key1, key4)
	}
	if key1 == key5 {
		t.Fatalf("different Status should produce different key: %+v vs %+v", key1, key5)
	}
}

func TestJoinableStringDeltaDetectsJSONStrings(t *testing.T) {
	tests := []struct {
		name     string
		delta    []byte
		wantJoin bool
	}{
		{
			name:     "simple string",
			delta:    []byte(`"hello"`),
			wantJoin: true,
		},
		{
			name:     "string with spaces",
			delta:    []byte(`" world"`),
			wantJoin: true,
		},
		{
			name:     "empty string",
			delta:    []byte(`""`),
			wantJoin: true,
		},
		{
			name:     "string with escapes",
			delta:    []byte(`"hello\nworld"`),
			wantJoin: true,
		},
		{
			name:     "JSON object",
			delta:    []byte(`{"key":"value"}`),
			wantJoin: false,
		},
		{
			name:     "JSON array",
			delta:    []byte(`["item1","item2"]`),
			wantJoin: false,
		},
		{
			name:     "JSON number",
			delta:    []byte(`42`),
			wantJoin: false,
		},
		{
			name:     "JSON boolean",
			delta:    []byte(`true`),
			wantJoin: false,
		},
		{
			name:     "JSON null",
			delta:    []byte(`null`),
			wantJoin: false,
		},
		{
			name:     "empty bytes",
			delta:    []byte(``),
			wantJoin: false,
		},
		{
			name:     "single quote",
			delta:    []byte(`"`),
			wantJoin: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isJoinableStringDelta(tt.delta)
			if got != tt.wantJoin {
				t.Fatalf("isJoinableStringDelta(%q) = %v, want %v", tt.delta, got, tt.wantJoin)
			}
		})
	}
}

func TestCanJoinDeltasOnlyJoinsStrings(t *testing.T) {
	tests := []struct {
		name      string
		prevDelta []byte
		nextDelta []byte
		wantJoin  bool
	}{
		{
			name:      "both strings",
			prevDelta: []byte(`"hello"`),
			nextDelta: []byte(`" world"`),
			wantJoin:  true,
		},
		{
			name:      "both empty strings",
			prevDelta: []byte(`""`),
			nextDelta: []byte(`""`),
			wantJoin:  true,
		},
		{
			name:      "prev string next object",
			prevDelta: []byte(`"hello"`),
			nextDelta: []byte(`{"key":"value"}`),
			wantJoin:  false,
		},
		{
			name:      "prev object next string",
			prevDelta: []byte(`{"key":"value"}`),
			nextDelta: []byte(`"hello"`),
			wantJoin:  false,
		},
		{
			name:      "both objects",
			prevDelta: []byte(`{"a":1}`),
			nextDelta: []byte(`{"b":2}`),
			wantJoin:  false,
		},
		{
			name:      "both arrays",
			prevDelta: []byte(`["a"]`),
			nextDelta: []byte(`["b"]`),
			wantJoin:  false,
		},
		{
			name:      "both numbers",
			prevDelta: []byte(`42`),
			nextDelta: []byte(`43`),
			wantJoin:  false,
		},
		{
			name:      "both booleans",
			prevDelta: []byte(`true`),
			nextDelta: []byte(`false`),
			wantJoin:  false,
		},
		{
			name:      "both null",
			prevDelta: []byte(`null`),
			nextDelta: []byte(`null`),
			wantJoin:  false,
		},
		{
			name:      "prev null next string",
			prevDelta: []byte(`null`),
			nextDelta: []byte(`"hello"`),
			wantJoin:  false,
		},
		{
			name:      "string with newlines",
			prevDelta: []byte(`"line1\n"`),
			nextDelta: []byte(`"line2\n"`),
			wantJoin:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canJoinDeltas(tt.prevDelta, tt.nextDelta)
			if got != tt.wantJoin {
				t.Fatalf("canJoinDeltas(%q, %q) = %v, want %v", tt.prevDelta, tt.nextDelta, got, tt.wantJoin)
			}
		})
	}
}

func TestUpstreamFIFOPreservesOrder(t *testing.T) {
	identity := upstreamIdentity{layer: "executor", id: "node_a"}
	fifo := newUpstreamFIFO(identity, 10)

	events := []Event{
		{EventType: EventLLMOutputDelta, From: Source{Layer: "executor", ID: "node_a"}, Status: StatusRunning, Delta: json.RawMessage(`"first"`)},
		{EventType: EventLLMOutputDelta, From: Source{Layer: "executor", ID: "node_a"}, Status: StatusRunning, Delta: json.RawMessage(`"second"`)},
		{EventType: EventLLMOutputDelta, From: Source{Layer: "executor", ID: "node_a"}, Status: StatusRunning, Delta: json.RawMessage(`"third"`)},
	}

	for _, e := range events {
		if err := fifo.enqueue(e); err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}
	}

	if fifo.len() != 3 {
		t.Fatalf("len after 3 enqueues = %d, want 3", fifo.len())
	}

	for i, wantEvent := range events {
		gotEvent, ok := fifo.pop()
		if !ok {
			t.Fatalf("pop %d = not ok", i)
		}
		if gotEvent.EventType != wantEvent.EventType || !bytes.Equal(gotEvent.Delta, wantEvent.Delta) {
			t.Fatalf("pop %d = %+v, want %+v", i, gotEvent, wantEvent)
		}
	}

	_, ok := fifo.pop()
	if ok {
		t.Fatalf("pop on empty FIFO should return not ok")
	}
}

func TestUpstreamFIFOTryJoinAtHeadJoinsStringDeltas(t *testing.T) {
	identity := upstreamIdentity{layer: "executor", id: "node_a"}
	fifo := newUpstreamFIFO(identity, 10)

	headEvent := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}
	nextEvent := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`" world"`),
	}

	if err := fifo.enqueue(headEvent); err != nil {
		t.Fatalf("enqueue head: %v", err)
	}

	joined := fifo.tryJoinAtHead(nextEvent)
	if !joined {
		t.Fatalf("tryJoinAtHead = false, want true")
	}

	if fifo.len() != 1 {
		t.Fatalf("len after join = %d, want 1", fifo.len())
	}

	result, ok := fifo.peek()
	if !ok {
		t.Fatalf("peek after join = not ok")
	}

	// The joined delta should be "hello world"
	want := []byte(`"hello world"`)
	if string(result.Delta) != string(want) {
		t.Fatalf("joined delta = %s, want %s", result.Delta, want)
	}
}

func TestUpstreamFIFOTryJoinRejectsNonStringDeltas(t *testing.T) {
	identity := upstreamIdentity{layer: "executor", id: "node_a"}
	fifo := newUpstreamFIFO(identity, 10)

	headEvent := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`{"key":"value"}`),
	}
	nextEvent := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"text"`),
	}

	if err := fifo.enqueue(headEvent); err != nil {
		t.Fatalf("enqueue head: %v", err)
	}

	joined := fifo.tryJoinAtHead(nextEvent)
	if joined {
		t.Fatalf("tryJoinAtHead with non-string = true, want false")
	}

	if fifo.len() != 1 {
		t.Fatalf("len after failed join = %d, want 1", fifo.len())
	}
}

func TestUpstreamFIFOTryJoinRejectsDifferentJoinKey(t *testing.T) {
	identity := upstreamIdentity{layer: "executor", id: "node_a"}
	fifo := newUpstreamFIFO(identity, 10)

	headEvent := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}
	// Different event type - should not join
	nextEventDiffType := Event{
		EventType: EventLLMReasoningDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`" world"`),
	}
	// Different status - should not join
	nextEventDiffStatus := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusCompleted,
		Delta:     json.RawMessage(`" world"`),
	}
	// Different upstream ID - should not join
	nextEventDiffUpstream := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_b"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`" world"`),
	}

	if err := fifo.enqueue(headEvent); err != nil {
		t.Fatalf("enqueue head: %v", err)
	}

	testCases := []struct {
		name      string
		nextEvent Event
	}{
		{"different event type", nextEventDiffType},
		{"different status", nextEventDiffStatus},
		{"different upstream", nextEventDiffUpstream},
	}

	for i, tc := range testCases {
		fifo := newUpstreamFIFO(identity, 10)
		if err := fifo.enqueue(headEvent); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		joined := fifo.tryJoinAtHead(tc.nextEvent)
		if joined {
			t.Fatalf("%s: tryJoinAtHead = true, want false", tc.name)
		}
	}
}

func TestUpstreamFIFORejectsWhenFull(t *testing.T) {
	identity := upstreamIdentity{layer: "executor", id: "node_a"}
	fifo := newUpstreamFIFO(identity, 2) // Small buffer for testing

	event1 := Event{EventType: EventStatusProgress, From: Source{Layer: "executor"}, Delta: json.RawMessage(`"1"`)}
	event2 := Event{EventType: EventStatusProgress, From: Source{Layer: "executor"}, Delta: json.RawMessage(`"2"`)}
	event3 := Event{EventType: EventStatusProgress, From: Source{Layer: "executor"}, Delta: json.RawMessage(`"3"`)}

	if err := fifo.enqueue(event1); err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	if err := fifo.enqueue(event2); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}

	// Third enqueue should fail
	if err := fifo.enqueue(event3); !errors.Is(err, ErrBufferFull) {
		t.Fatalf("enqueue 3 = %v, want ErrBufferFull", err)
	}

	if fifo.len() != 2 {
		t.Fatalf("len after full buffer = %d, want 2", fifo.len())
	}
}

func TestUpstreamFIFODefaultMaxDepth(t *testing.T) {
	// newUpstreamFIFO should use default depth of 1000 when passed 0 or negative
	identity := upstreamIdentity{layer: "test", id: "id"}

	fifo0 := newUpstreamFIFO(identity, 0)
	if fifo0.maxDepth != 1000 {
		t.Fatalf("maxDepth with 0 = %d, want 1000", fifo0.maxDepth)
	}

	fifoNeg := newUpstreamFIFO(identity, -5)
	if fifoNeg.maxDepth != 1000 {
		t.Fatalf("maxDepth with -5 = %d, want 1000", fifoNeg.maxDepth)
	}
}

func TestUpstreamFIFOPeekDoesNotRemove(t *testing.T) {
	identity := upstreamIdentity{layer: "executor", id: "node_a"}
	fifo := newUpstreamFIFO(identity, 10)

	event := Event{EventType: EventStatusProgress, From: Source{Layer: "executor"}, Delta: json.RawMessage(`"test"`)}
	if err := fifo.enqueue(event); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Peek multiple times
	for i := 0; i < 3; i++ {
		peeked, ok := fifo.peek()
		if !ok {
			t.Fatalf("peek %d = not ok", i)
		}
		if peeked.EventType != event.EventType {
			t.Fatalf("peek %d = %+v, want %+v", i, peeked, event)
		}
	}

	// FIFO should still have the event
	if fifo.len() != 1 {
		t.Fatalf("len after multiple peeks = %d, want 1", fifo.len())
	}

	// Pop should get the same event
	popped, ok := fifo.pop()
	if !ok {
		t.Fatalf("pop = not ok")
	}
	if popped.EventType != event.EventType {
		t.Fatalf("pop = %+v, want %+v", popped, event)
	}
}
