package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
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

func TestMergeHubTickEmitsBundleWithReadyEvents(t *testing.T) {
	downstream := New(Scope{}, DefaultConfig())
	t.Cleanup(downstream.Close)

	hub := newMergeHub(t.Context(), downstream, 10*time.Millisecond, 1000)
	defer hub.Stop()

	upstream1 := New(Scope{}, DefaultConfig())
	upstream2 := New(Scope{}, DefaultConfig())
	t.Cleanup(upstream1.Close)
	t.Cleanup(upstream2.Close)

	// Add upstreams to hub.
	id1 := upstreamIdentity{layer: "executor", id: "node_1"}
	id2 := upstreamIdentity{layer: "executor", id: "node_2"}

	if err := hub.addUpstream(t.Context(), upstream1, id1, Filter{}); err != nil {
		t.Fatalf("addUpstream 1: %v", err)
	}
	if err := hub.addUpstream(t.Context(), upstream2, id2, Filter{}); err != nil {
		t.Fatalf("addUpstream 2: %v", err)
	}

	// Emit events from upstreams.
	if err := upstream1.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}); err != nil {
		t.Fatalf("Emit upstream1: %v", err)
	}

	if err := upstream2.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_2"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"world"`),
	}); err != nil {
		t.Fatalf("Emit upstream2: %v", err)
	}

	// Subscribe to downstream to receive bundle.
	sub, err := downstream.Subscribe(Filter{EventTypes: []string{EventMergeBundle}})
	if err != nil {
		t.Fatalf("Subscribe downstream: %v", err)
	}

	// Wait for bundle event on the next tick.
	bundleEvent, ok, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !ok {
		t.Fatalf("Next = not ok, want bundle event")
	}

	if bundleEvent.EventType != EventMergeBundle {
		t.Fatalf("event type = %q, want %q", bundleEvent.EventType, EventMergeBundle)
	}

	// Decode and verify bundle contents.
	bundle, err := DecodeBundlePayload(bundleEvent.Delta)
	if err != nil {
		t.Fatalf("DecodeBundlePayload: %v", err)
	}

	if len(bundle.NestedEvents) != 2 {
		t.Fatalf("nested events count = %d, want 2", len(bundle.NestedEvents))
	}

	if bundle.TickID != 1 {
		t.Fatalf("tick ID = %d, want 1", bundle.TickID)
	}
}

func TestMergeHubJoinsConsecutiveStringDeltas(t *testing.T) {
	downstream := New(Scope{}, DefaultConfig())
	t.Cleanup(downstream.Close)

	hub := newMergeHub(t.Context(), downstream, 10*time.Millisecond, 1000)
	defer hub.Stop()

	upstream := New(Scope{}, DefaultConfig())
	t.Cleanup(upstream.Close)

	id := upstreamIdentity{layer: "executor", id: "node_a"}
	if err := hub.addUpstream(t.Context(), upstream, id, Filter{}); err != nil {
		t.Fatalf("addUpstream: %v", err)
	}

	// Emit multiple string deltas with same type/status (should join).
	if err := upstream.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}); err != nil {
		t.Fatalf("Emit 1: %v", err)
	}

	if err := upstream.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`" world"`),
	}); err != nil {
		t.Fatalf("Emit 2: %v", err)
	}

	// Subscribe to downstream.
	sub, err := downstream.Subscribe(Filter{EventTypes: []string{EventMergeBundle}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Wait for bundle.
	bundleEvent, ok, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !ok {
		t.Fatalf("Next = not ok")
	}

	bundle, err := DecodeBundlePayload(bundleEvent.Delta)
	if err != nil {
		t.Fatalf("DecodeBundlePayload: %v", err)
	}

	// Should have one joined event instead of two.
	if len(bundle.NestedEvents) != 1 {
		t.Fatalf("nested events count = %d, want 1 (should be joined)", len(bundle.NestedEvents))
	}

	// Verify the joined delta.
	wantDelta := []byte(`"hello world"`)
	if !bytes.Equal(bundle.NestedEvents[0].Delta, wantDelta) {
		t.Fatalf("joined delta = %s, want %s", bundle.NestedEvents[0].Delta, wantDelta)
	}
}

func TestMergeHubKeepsNonJoinableDeltasQueued(t *testing.T) {
	downstream := New(Scope{}, DefaultConfig())
	t.Cleanup(downstream.Close)

	hub := newMergeHub(t.Context(), downstream, 10*time.Millisecond, 1000)
	defer hub.Stop()

	upstream := New(Scope{}, DefaultConfig())
	t.Cleanup(upstream.Close)

	id := upstreamIdentity{layer: "executor", id: "node_a"}
	if err := hub.addUpstream(t.Context(), upstream, id, Filter{}); err != nil {
		t.Fatalf("addUpstream: %v", err)
	}

	// Emit events with different types (should NOT join).
	if err := upstream.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"output"`),
	}); err != nil {
		t.Fatalf("Emit output: %v", err)
	}

	if err := upstream.Emit(t.Context(), Event{
		EventType: EventLLMReasoningDelta,
		From:      Source{Layer: "executor", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"reasoning"`),
	}); err != nil {
		t.Fatalf("Emit reasoning: %v", err)
	}

	// Subscribe to downstream.
	sub, err := downstream.Subscribe(Filter{EventTypes: []string{EventMergeBundle}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// First tick should have only the output event (first in FIFO).
	bundleEvent1, ok, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next 1: %v", err)
	}
	if !ok {
		t.Fatalf("Next 1 = not ok")
	}

	bundle1, err := DecodeBundlePayload(bundleEvent1.Delta)
	if err != nil {
		t.Fatalf("DecodeBundlePayload 1: %v", err)
	}

	if len(bundle1.NestedEvents) != 1 {
		t.Fatalf("first bundle events = %d, want 1", len(bundle1.NestedEvents))
	}
	if bundle1.NestedEvents[0].EventType != EventLLMOutputDelta {
		t.Fatalf("first event type = %q, want output", bundle1.NestedEvents[0].EventType)
	}

	// Second tick should have the reasoning event (delayed from previous tick).
	bundleEvent2, ok, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next 2: %v", err)
	}
	if !ok {
		t.Fatalf("Next 2 = not ok")
	}

	bundle2, err := DecodeBundlePayload(bundleEvent2.Delta)
	if err != nil {
		t.Fatalf("DecodeBundlePayload 2: %v", err)
	}

	if len(bundle2.NestedEvents) != 1 {
		t.Fatalf("second bundle events = %d, want 1", len(bundle2.NestedEvents))
	}
	if bundle2.NestedEvents[0].EventType != EventLLMReasoningDelta {
		t.Fatalf("second event type = %q, want reasoning", bundle2.NestedEvents[0].EventType)
	}
}

// TestMergeFromDefaultBehaviorUnchanged verifies that MergeFrom without config
// works exactly as before (direct forwarding, not hub mode).
func TestMergeFromDefaultBehaviorUnchanged(t *testing.T) {
	parent := New(Scope{RequestID: "req_1"}, DefaultConfig())
	child := New(Scope{NodeID: "node_1"}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(child.Close)

	sub, err := parent.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	merge, err := parent.MergeFrom(t.Context(), child, Filter{})
	if err != nil {
		t.Fatalf("MergeFrom: %v", err)
	}
	defer merge.Stop()

	// Emit a direct event (not bundled).
	if err := child.Emit(t.Context(), Event{
		EventType: EventExecutorExecuteStarted,
		From:      Source{Layer: "executor", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	event, ok, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !ok {
		t.Fatalf("Next = not ok")
	}

	// Should be the direct event, not a bundle.
	if event.EventType != EventExecutorExecuteStarted {
		t.Fatalf("EventType = %q, want executor.execute.started", event.EventType)
	}
	if string(event.Delta) != `"hello"` {
		t.Fatalf("Delta = %s, want \"hello\"", event.Delta)
	}
}

// TestMergeFromWithConfigHubModeEmitsBundles verifies that hub mode (with
// TickDuration > 0) emits bundle events instead of direct events.
func TestMergeFromWithConfigHubModeEmitsBundles(t *testing.T) {
	parent := New(Scope{RequestID: "req_1"}, DefaultConfig())
	child := New(Scope{NodeID: "node_1"}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(child.Close)

	sub, err := parent.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	config := MergeWindowConfig{
		TickDuration:           10 * time.Millisecond,
		PerUpstreamBufferDepth: 1000,
	}
	merge, err := parent.MergeWithConfig(t.Context(), child, Filter{}, config)
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}
	defer merge.Stop()

	// Emit an event.
	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Should receive a bundle event (not direct).
	event, ok, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !ok {
		t.Fatalf("Next = not ok")
	}

	// Event should be a bundle event type.
	if event.EventType != EventMergeBundle {
		t.Fatalf("EventType = %q, want merge.bundle", event.EventType)
	}

	// Decode bundle to verify nested event.
	bundle, err := DecodeBundlePayload(event.Delta)
	if err != nil {
		t.Fatalf("DecodeBundlePayload: %v", err)
	}

	if len(bundle.NestedEvents) != 1 {
		t.Fatalf("NestedEvents = %d, want 1", len(bundle.NestedEvents))
	}
	if bundle.NestedEvents[0].EventType != EventLLMOutputDelta {
		t.Fatalf("Nested EventType = %q, want llm.output.delta", bundle.NestedEvents[0].EventType)
	}
}

// TestMergeFromWithConfigHubRespectsFilters verifies that hub mode respects
// filters applied to upstream subscriptions.
func TestMergeFromWithConfigHubRespectsFilters(t *testing.T) {
	parent := New(Scope{RequestID: "req_1"}, DefaultConfig())
	child := New(Scope{NodeID: "node_1"}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(child.Close)

	sub, err := parent.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	config := MergeWindowConfig{
		TickDuration:           10 * time.Millisecond,
		PerUpstreamBufferDepth: 1000,
	}

	// Filter to only LLM events.
	filter := Filter{Prefixes: []string{"llm"}}
	merge, err := parent.MergeWithConfig(t.Context(), child, filter, config)
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}
	defer merge.Stop()

	// Emit two events: one LLM (should pass), one executor (should be filtered out).
	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"llm_delta"`),
	}); err != nil {
		t.Fatalf("Emit llm: %v", err)
	}

	if err := child.Emit(t.Context(), Event{
		EventType: EventExecutorExecuteStarted,
		From:      Source{Layer: "executor", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("Emit executor: %v", err)
	}

	// Should receive only the LLM event in a bundle (executor is filtered).
	event, ok, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !ok {
		t.Fatalf("Next = not ok")
	}

	bundle, err := DecodeBundlePayload(event.Delta)
	if err != nil {
		t.Fatalf("DecodeBundlePayload: %v", err)
	}

	if len(bundle.NestedEvents) != 1 {
		t.Fatalf("NestedEvents = %d, want 1 (executor filtered out)", len(bundle.NestedEvents))
	}
	if bundle.NestedEvents[0].EventType != EventLLMOutputDelta {
		t.Fatalf("EventType = %q, want llm.output.delta", bundle.NestedEvents[0].EventType)
	}
}

// TestMergeFromWithConfigHubContextCancellation verifies that context cancellation
// stops the hub goroutines.
func TestMergeFromWithConfigHubContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	parent := New(Scope{RequestID: "req_1"}, DefaultConfig())
	child := New(Scope{NodeID: "node_1"}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(child.Close)

	config := MergeWindowConfig{
		TickDuration:           10 * time.Millisecond,
		PerUpstreamBufferDepth: 1000,
	}
	merge, err := parent.MergeWithConfig(ctx, child, Filter{}, config)
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}

	// Emit an event to ensure hub is running.
	if err := child.Emit(ctx, Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"test"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	// Cancel context to stop hub.
	cancel()

	// Wait for merge to finish.
	select {
	case <-merge.Done():
		// Expected: Done closes when context is canceled.
	case <-time.After(1 * time.Second):
		t.Fatalf("merge.Done() did not close after context cancellation")
	}
}

// TestMergeFromWithConfigMergeStopStopsHub verifies that calling Merge.Stop()
// stops the hub and its goroutines.
func TestMergeFromWithConfigMergeStopStopsHub(t *testing.T) {
	parent := New(Scope{RequestID: "req_1"}, DefaultConfig())
	child := New(Scope{NodeID: "node_1"}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(child.Close)

	config := MergeWindowConfig{
		TickDuration:           10 * time.Millisecond,
		PerUpstreamBufferDepth: 1000,
	}
	merge, err := parent.MergeWithConfig(t.Context(), child, Filter{}, config)
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}

	// Call Stop to terminate the merge and hub.
	merge.Stop()

	// Wait for merge to finish.
	select {
	case <-merge.Done():
		// Expected: Done closes when Stop() is called.
	case <-time.After(1 * time.Second):
		t.Fatalf("merge.Done() did not close after Stop()")
	}
}

func TestMergeFromWithConfigRejectsNegativeTickDuration(t *testing.T) {
	parent := New(Scope{}, DefaultConfig())
	child := New(Scope{}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(child.Close)

	_, err := parent.MergeWithConfig(t.Context(), child, Filter{}, MergeWindowConfig{
		TickDuration: -time.Millisecond,
	})
	if !errors.Is(err, ErrInvalidMerge) {
		t.Fatalf("MergeFromWithConfig negative tick err = %v, want ErrInvalidMerge", err)
	}
}

func TestMergeFromWithConfigHubEmptyUpstreamCloseStopsMerge(t *testing.T) {
	parent := New(Scope{}, DefaultConfig())
	child := New(Scope{}, DefaultConfig())
	t.Cleanup(parent.Close)

	merge, err := parent.MergeWithConfig(t.Context(), child, Filter{}, MergeWindowConfig{
		TickDuration: time.Hour,
	})
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}

	child.Close()

	select {
	case <-merge.Done():
	case <-time.After(time.Second):
		t.Fatalf("merge.Done() did not close after empty upstream closed")
	}
}

func TestMergeFromWithConfigHubDrainsBufferedEventsOnUpstreamClose(t *testing.T) {
	parent := New(Scope{RequestID: "req_1"}, DefaultConfig())
	child := New(Scope{NodeID: "node_1"}, DefaultConfig())
	t.Cleanup(parent.Close)

	sub, err := parent.Subscribe(Filter{EventTypes: []string{EventMergeBundle}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	merge, err := parent.MergeWithConfig(t.Context(), child, Filter{}, MergeWindowConfig{
		TickDuration: time.Hour,
	})
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}

	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"output"`),
	}); err != nil {
		t.Fatalf("Emit output: %v", err)
	}
	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMReasoningDelta,
		From:      Source{Layer: "executor", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"reasoning"`),
	}); err != nil {
		t.Fatalf("Emit reasoning: %v", err)
	}
	child.Close()

	first := receiveEvent(t, sub.Events())
	second := receiveEvent(t, sub.Events())
	firstBundle, err := DecodeBundlePayload(first.Delta)
	if err != nil {
		t.Fatalf("Decode first bundle: %v", err)
	}
	secondBundle, err := DecodeBundlePayload(second.Delta)
	if err != nil {
		t.Fatalf("Decode second bundle: %v", err)
	}
	if len(firstBundle.NestedEvents) != 1 || firstBundle.NestedEvents[0].EventType != EventLLMOutputDelta {
		t.Fatalf("first drained bundle = %+v, want output", firstBundle.NestedEvents)
	}
	if len(secondBundle.NestedEvents) != 1 || secondBundle.NestedEvents[0].EventType != EventLLMReasoningDelta {
		t.Fatalf("second drained bundle = %+v, want reasoning", secondBundle.NestedEvents)
	}

	select {
	case <-merge.Done():
	case <-time.After(time.Second):
		t.Fatalf("merge.Done() did not close after draining upstream")
	}
}

func TestMergeFromWithConfigHubNestedEventsUseMergedScopeAndMetadata(t *testing.T) {
	parent := New(Scope{RequestID: "req_1"}, DefaultConfig())
	child := New(Scope{NodeID: "node_1"}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(child.Close)

	sub, err := parent.Subscribe(Filter{EventTypes: []string{EventMergeBundle}})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	merge, err := parent.MergeWithConfig(t.Context(), child, Filter{}, MergeWindowConfig{
		TickDuration: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}
	defer merge.Stop()

	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"output"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	bundleEvent := receiveEvent(t, sub.Events())
	bundle, err := DecodeBundlePayload(bundleEvent.Delta)
	if err != nil {
		t.Fatalf("DecodeBundlePayload: %v", err)
	}
	if len(bundle.NestedEvents) != 1 {
		t.Fatalf("NestedEvents = %d, want 1", len(bundle.NestedEvents))
	}
	nested := bundle.NestedEvents[0]
	if nested.Scope.RequestID != "req_1" || nested.Scope.NodeID != "node_1" {
		t.Fatalf("nested scope = %+v, want parent request and child node", nested.Scope)
	}
	if nested.SequenceNumber != 0 {
		t.Fatalf("nested sequence = %d, want cleared upstream sequence", nested.SequenceNumber)
	}
	if nested.Metadata["upstream_sequence_number"] != float64(1) {
		t.Fatalf("nested upstream sequence metadata = %#v, want 1", nested.Metadata["upstream_sequence_number"])
	}
}

func TestMergeFromWithConfigHubErrorVisibleThroughMergeErr(t *testing.T) {
	parent := New(Scope{}, DefaultConfig())
	child := New(Scope{}, DefaultConfig())
	t.Cleanup(child.Close)

	merge, err := parent.MergeWithConfig(t.Context(), child, Filter{}, MergeWindowConfig{
		TickDuration: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}
	defer merge.Stop()

	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "executor", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"output"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	parent.Close()

	deadline := time.After(time.Second)
	for {
		if errors.Is(merge.Err(), ErrClosed) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("merge.Err() = %v, want ErrClosed", merge.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
