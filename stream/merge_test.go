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
	mergeA, err := parent.mergeFrom(t.Context(), childA, Filter{})
	if err != nil {
		t.Fatalf("mergeFrom childA: %v", err)
	}
	mergeB, err := parent.mergeFrom(t.Context(), childB, Filter{})
	if err != nil {
		t.Fatalf("mergeFrom childB: %v", err)
	}
	defer mergeA.Stop()
	defer mergeB.Stop()

	if err := childA.Emit(t.Context(), Event{
		EventType: EventAgentExecuteStarted,
		From:      Source{Layer: "agent", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`{"stage":"execute"}`),
	}); err != nil {
		t.Fatalf("Emit childA: %v", err)
	}
	if err := childB.Emit(t.Context(), Event{
		EventType: EventAgentExecuteCompleted,
		From:      Source{Layer: "agent", ID: "node_b"},
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
		From:      Source{Layer: "agent"},
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
	merge, err := parent.mergeFrom(t.Context(), child, Filter{Prefixes: []string{"llm."}}, WithReplayFrom(1))
	if err != nil {
		t.Fatalf("mergeFrom: %v", err)
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
	if _, err := s.mergeFrom(t.Context(), nil, Filter{}); !errors.Is(err, ErrInvalidMerge) {
		t.Fatalf("mergeFrom nil err = %v, want ErrInvalidMerge", err)
	}
	if _, err := s.mergeFrom(t.Context(), s, Filter{}); !errors.Is(err, ErrInvalidMerge) {
		t.Fatalf("mergeFrom self err = %v, want ErrInvalidMerge", err)
	}
}

func TestUpstreamIdentitySameSourceShareIdentity(t *testing.T) {
	src1 := Source{Layer: "agent", ID: "node_a"}
	src2 := Source{Layer: "agent", ID: "node_a"}
	src3 := Source{Layer: "agent", ID: "node_b"}
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
		From:      Source{Layer: "agent", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}
	event2 := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`" world"`),
	}
	event3 := Event{
		EventType: EventLLMReasoningDelta,
		From:      Source{Layer: "agent", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}
	event4 := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_b"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}
	event5 := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_a"},
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
	identity := upstreamIdentity{layer: "agent", id: "node_a"}
	fifo := newUpstreamFIFO(identity, 10)

	events := []Event{
		{EventType: EventLLMOutputDelta, From: Source{Layer: "agent", ID: "node_a"}, Status: StatusRunning, Delta: json.RawMessage(`"first"`)},
		{EventType: EventLLMOutputDelta, From: Source{Layer: "agent", ID: "node_a"}, Status: StatusRunning, Delta: json.RawMessage(`"second"`)},
		{EventType: EventLLMOutputDelta, From: Source{Layer: "agent", ID: "node_a"}, Status: StatusRunning, Delta: json.RawMessage(`"third"`)},
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

func TestUpstreamFIFOPopJoinedHeadJoinsStringDeltas(t *testing.T) {
	identity := upstreamIdentity{layer: "agent", id: "node_a"}
	fifo := newUpstreamFIFO(identity, 10)

	for _, event := range []Event{
		{
			EventType: EventLLMOutputDelta,
			From:      Source{Layer: "agent", ID: "node_a"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(`"hello"`),
		},
		{
			EventType: EventLLMOutputDelta,
			From:      Source{Layer: "agent", ID: "node_a"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(`" world"`),
		},
		{
			EventType: EventLLMOutputDelta,
			From:      Source{Layer: "agent", ID: "node_a"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(`"!"`),
		},
	} {
		if err := fifo.enqueue(event); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}

	joined, ok := fifo.popJoinedHead()
	if !ok {
		t.Fatalf("popJoinedHead = not ok")
	}
	if string(joined.Delta) != `"hello world!"` {
		t.Fatalf("joined delta = %s, want %s", joined.Delta, `"hello world!"`)
	}
	if fifo.len() != 0 {
		t.Fatalf("len after joined pop = %d, want 0", fifo.len())
	}
	if _, ok := fifo.popJoinedHead(); ok {
		t.Fatalf("popJoinedHead on empty FIFO = ok, want not ok")
	}
}

func TestUpstreamFIFOPopJoinedHeadKeepsNonJoinableQueued(t *testing.T) {
	identity := upstreamIdentity{layer: "agent", id: "node_a"}
	headEvent := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}

	tests := []struct {
		name string
		next Event
	}{
		{
			name: "non-string delta",
			next: Event{
				EventType: EventLLMOutputDelta,
				From:      Source{Layer: "agent", ID: "node_a"},
				Status:    StatusRunning,
				Delta:     json.RawMessage(`{"text":"object"}`),
			},
		},
		{
			name: "different event type",
			next: Event{
				EventType: EventLLMReasoningDelta,
				From:      Source{Layer: "agent", ID: "node_a"},
				Status:    StatusRunning,
				Delta:     json.RawMessage(`"world"`),
			},
		},
		{
			name: "different status",
			next: Event{
				EventType: EventLLMOutputDelta,
				From:      Source{Layer: "agent", ID: "node_a"},
				Status:    StatusCompleted,
				Delta:     json.RawMessage(`"world"`),
			},
		},
		{
			name: "different upstream",
			next: Event{
				EventType: EventLLMOutputDelta,
				From:      Source{Layer: "agent", ID: "node_b"},
				Status:    StatusRunning,
				Delta:     json.RawMessage(`"world"`),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fifo := newUpstreamFIFO(identity, 10)
			if err := fifo.enqueue(headEvent); err != nil {
				t.Fatalf("enqueue head: %v", err)
			}
			if err := fifo.enqueue(tc.next); err != nil {
				t.Fatalf("enqueue next: %v", err)
			}

			gotHead, ok := fifo.popJoinedHead()
			if !ok {
				t.Fatalf("popJoinedHead = not ok")
			}
			if string(gotHead.Delta) != `"hello"` {
				t.Fatalf("head delta = %s, want %s", gotHead.Delta, `"hello"`)
			}
			if fifo.len() != 1 {
				t.Fatalf("len after non-joinable pop = %d, want 1", fifo.len())
			}
			queued, ok := fifo.pop()
			if !ok {
				t.Fatalf("queued pop = not ok")
			}
			if queued.EventType != tc.next.EventType || queued.Status != tc.next.Status || queued.From != tc.next.From || !bytes.Equal(queued.Delta, tc.next.Delta) {
				t.Fatalf("queued event = %+v, want %+v", queued, tc.next)
			}
		})
	}
}

func TestUpstreamFIFORejectsWhenFull(t *testing.T) {
	identity := upstreamIdentity{layer: "agent", id: "node_a"}
	fifo := newUpstreamFIFO(identity, 2) // Small buffer for testing

	event1 := Event{EventType: EventStatusProgress, From: Source{Layer: "agent"}, Delta: json.RawMessage(`"1"`)}
	event2 := Event{EventType: EventStatusProgress, From: Source{Layer: "agent"}, Delta: json.RawMessage(`"2"`)}
	event3 := Event{EventType: EventStatusProgress, From: Source{Layer: "agent"}, Delta: json.RawMessage(`"3"`)}

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
	// newUpstreamFIFO should use the default depth when passed 0 or negative.
	identity := upstreamIdentity{layer: "test", id: "id"}

	fifo0 := newUpstreamFIFO(identity, 0)
	if fifo0.maxDepth != defaultMergePerUpstreamBufferDepth {
		t.Fatalf("maxDepth with 0 = %d, want %d", fifo0.maxDepth, defaultMergePerUpstreamBufferDepth)
	}

	fifoNeg := newUpstreamFIFO(identity, -5)
	if fifoNeg.maxDepth != defaultMergePerUpstreamBufferDepth {
		t.Fatalf("maxDepth with -5 = %d, want %d", fifoNeg.maxDepth, defaultMergePerUpstreamBufferDepth)
	}
}

func TestMergeHubTickEmitsBundleWithReadyEvents(t *testing.T) {
	downstream := New(Scope{}, DefaultConfig())
	t.Cleanup(downstream.Close)

	hub := newMergeHub(t.Context(), downstream, time.Hour, defaultMergePerUpstreamBufferDepth)
	defer hub.Stop()

	id1 := upstreamIdentity{layer: "agent", id: "node_1"}
	id2 := upstreamIdentity{layer: "agent", id: "node_2"}
	hub.beginRegistration()
	if err := hub.registerPendingUpstream(id1, nil); err != nil {
		t.Fatalf("registerPendingUpstream node_1: %v", err)
	}
	hub.beginRegistration()
	if err := hub.registerPendingUpstream(id2, nil); err != nil {
		t.Fatalf("registerPendingUpstream node_2: %v", err)
	}

	if err := hub.enqueue(id1, Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"hello"`),
	}); err != nil {
		t.Fatalf("enqueue node_1: %v", err)
	}
	if err := hub.enqueue(id2, Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_2"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"world"`),
	}); err != nil {
		t.Fatalf("enqueue node_2: %v", err)
	}

	sub, err := downstream.Subscribe(Filter{EventTypes: []string{EventMergeBundle}}, WithRawStream())
	if err != nil {
		t.Fatalf("Subscribe downstream: %v", err)
	}
	defer sub.Cancel()

	if !hub.flushTick() {
		t.Fatalf("flushTick = false, want bundle event")
	}
	bundleEvent := receiveEvent(t, sub.Events())

	if bundleEvent.EventType != EventMergeBundle {
		t.Fatalf("event type = %q, want %q", bundleEvent.EventType, EventMergeBundle)
	}

	// Decode and verify bundle contents.
	bundle, err := DecodeEventBatch(bundleEvent.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch: %v", err)
	}

	if len(bundle.Events) != 2 {
		t.Fatalf("events count = %d, want 2", len(bundle.Events))
	}

	if bundle.TickID != 1 {
		t.Fatalf("tick ID = %d, want 1", bundle.TickID)
	}
}

func TestMergeHubJoinsConsecutiveStringDeltas(t *testing.T) {
	downstream := New(Scope{}, DefaultConfig())
	t.Cleanup(downstream.Close)

	hub := newMergeHub(t.Context(), downstream, time.Hour, defaultMergePerUpstreamBufferDepth)
	defer hub.Stop()

	id := upstreamIdentity{layer: "agent", id: "node_a"}
	hub.beginRegistration()
	if err := hub.registerPendingUpstream(id, nil); err != nil {
		t.Fatalf("registerPendingUpstream: %v", err)
	}

	if err := hub.enqueue(id, Event{
		EventType:   EventLLMOutputDelta,
		From:        Source{Layer: "agent", ID: "node_a"},
		LogicalTime: 1,
		Status:      StatusRunning,
		Delta:       json.RawMessage(`"hello"`),
	}); err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}

	if err := hub.enqueue(id, Event{
		EventType:   EventLLMOutputDelta,
		From:        Source{Layer: "agent", ID: "node_a"},
		LogicalTime: 2,
		Status:      StatusRunning,
		Delta:       json.RawMessage(`" world"`),
	}); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}
	if err := hub.enqueue(id, Event{
		EventType:   EventLLMOutputDelta,
		From:        Source{Layer: "agent", ID: "node_a"},
		LogicalTime: 3,
		Status:      StatusRunning,
		Delta:       json.RawMessage(`"!"`),
	}); err != nil {
		t.Fatalf("enqueue 3: %v", err)
	}

	sub, err := downstream.Subscribe(Filter{EventTypes: []string{EventMergeBundle}}, WithRawStream())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	if !hub.flushTick() {
		t.Fatalf("flushTick = false, want bundle")
	}
	bundleEvent := receiveEvent(t, sub.Events())

	bundle, err := DecodeEventBatch(bundleEvent.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch: %v", err)
	}

	// Should have one joined event instead of three.
	if len(bundle.Events) != 1 {
		t.Fatalf("events count = %d, want 1 (should be joined)", len(bundle.Events))
	}

	// Verify the joined delta.
	wantDelta := []byte(`"hello world!"`)
	if !bytes.Equal(bundle.Events[0].Delta, wantDelta) {
		t.Fatalf("joined delta = %s, want %s", bundle.Events[0].Delta, wantDelta)
	}
	if bundle.Events[0].LogicalTime != 1 {
		t.Fatalf("joined logical time = %d, want first event logical time 1", bundle.Events[0].LogicalTime)
	}
}

func TestMergeHubJoinsOnlyAdjacentSameKeyDeltas(t *testing.T) {
	downstream := New(Scope{}, DefaultConfig())
	t.Cleanup(downstream.Close)

	hub := newMergeHub(t.Context(), downstream, time.Hour, defaultMergePerUpstreamBufferDepth)
	defer hub.Stop()

	identity := upstreamIdentity{layer: "agent", id: "node_a"}
	hub.beginRegistration()
	if err := hub.registerPendingUpstream(identity, nil); err != nil {
		t.Fatalf("registerPendingUpstream: %v", err)
	}

	sub, err := downstream.Subscribe(Filter{EventTypes: []string{EventMergeBundle}}, WithRawStream())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	for _, event := range []Event{
		{
			EventType: EventLLMOutputDelta,
			From:      Source{Layer: "agent", ID: "node_a"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(`"hello "`),
		},
		{
			EventType: EventLLMReasoningDelta,
			From:      Source{Layer: "agent", ID: "node_a"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(`"thinking"`),
		},
		{
			EventType: EventLLMOutputDelta,
			From:      Source{Layer: "agent", ID: "node_a"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(`"world"`),
		},
	} {
		if err := hub.enqueue(identity, event); err != nil {
			t.Fatalf("enqueue %s: %v", event.EventType, err)
		}
	}

	want := []struct {
		eventType string
		delta     string
	}{
		{EventLLMOutputDelta, `"hello "`},
		{EventLLMReasoningDelta, `"thinking"`},
		{EventLLMOutputDelta, `"world"`},
	}

	for tick, wantEvent := range want {
		if !hub.flushTick() {
			t.Fatalf("flushTick %d = false, want bundle", tick+1)
		}
		bundleEvent := receiveEvent(t, sub.Events())
		bundle, err := DecodeEventBatch(bundleEvent.Delta)
		if err != nil {
			t.Fatalf("DecodeEventBatch %d: %v", tick+1, err)
		}
		if len(bundle.Events) != 1 {
			t.Fatalf("tick %d events = %d, want 1", tick+1, len(bundle.Events))
		}
		got := bundle.Events[0]
		if got.EventType != wantEvent.eventType || string(got.Delta) != wantEvent.delta {
			t.Fatalf("tick %d event = (%s, %s), want (%s, %s)", tick+1, got.EventType, got.Delta, wantEvent.eventType, wantEvent.delta)
		}
	}
}

func TestMergeHubStopsJoiningAtNonStringDelta(t *testing.T) {
	downstream := New(Scope{}, DefaultConfig())
	t.Cleanup(downstream.Close)

	hub := newMergeHub(t.Context(), downstream, time.Hour, defaultMergePerUpstreamBufferDepth)
	defer hub.Stop()

	identity := upstreamIdentity{layer: "agent", id: "node_a"}
	hub.beginRegistration()
	if err := hub.registerPendingUpstream(identity, nil); err != nil {
		t.Fatalf("registerPendingUpstream: %v", err)
	}

	sub, err := downstream.Subscribe(Filter{EventTypes: []string{EventMergeBundle}}, WithRawStream())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	for _, event := range []Event{
		{
			EventType: EventLLMOutputDelta,
			From:      Source{Layer: "agent", ID: "node_a"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(`"first"`),
		},
		{
			EventType: EventLLMOutputDelta,
			From:      Source{Layer: "agent", ID: "node_a"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(`{"text":"object"}`),
		},
		{
			EventType: EventLLMOutputDelta,
			From:      Source{Layer: "agent", ID: "node_a"},
			Status:    StatusRunning,
			Delta:     json.RawMessage(`"third"`),
		},
	} {
		if err := hub.enqueue(identity, event); err != nil {
			t.Fatalf("enqueue %s: %v", event.EventType, err)
		}
	}

	for tick, wantDelta := range []string{`"first"`, `{"text":"object"}`, `"third"`} {
		if !hub.flushTick() {
			t.Fatalf("flushTick %d = false, want bundle", tick+1)
		}
		bundleEvent := receiveEvent(t, sub.Events())
		bundle, err := DecodeEventBatch(bundleEvent.Delta)
		if err != nil {
			t.Fatalf("DecodeEventBatch %d: %v", tick+1, err)
		}
		if len(bundle.Events) != 1 || string(bundle.Events[0].Delta) != wantDelta {
			t.Fatalf("tick %d events = %+v, want delta %s", tick+1, bundle.Events, wantDelta)
		}
	}
}

func TestMergeHubKeepsNonJoinableDeltasQueued(t *testing.T) {
	downstream := New(Scope{}, DefaultConfig())
	t.Cleanup(downstream.Close)

	hub := newMergeHub(t.Context(), downstream, time.Hour, defaultMergePerUpstreamBufferDepth)
	defer hub.Stop()

	id := upstreamIdentity{layer: "agent", id: "node_a"}
	hub.beginRegistration()
	if err := hub.registerPendingUpstream(id, nil); err != nil {
		t.Fatalf("registerPendingUpstream: %v", err)
	}

	if err := hub.enqueue(id, Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"output"`),
	}); err != nil {
		t.Fatalf("enqueue output: %v", err)
	}

	if err := hub.enqueue(id, Event{
		EventType: EventLLMReasoningDelta,
		From:      Source{Layer: "agent", ID: "node_a"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"reasoning"`),
	}); err != nil {
		t.Fatalf("enqueue reasoning: %v", err)
	}

	sub, err := downstream.Subscribe(Filter{EventTypes: []string{EventMergeBundle}}, WithRawStream())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	if !hub.flushTick() {
		t.Fatalf("first flushTick = false, want first bundle")
	}

	bundleEvent1 := receiveEvent(t, sub.Events())
	bundle1, err := DecodeEventBatch(bundleEvent1.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch 1: %v", err)
	}

	if len(bundle1.Events) != 1 {
		t.Fatalf("first bundle events = %d, want 1", len(bundle1.Events))
	}
	if bundle1.Events[0].EventType != EventLLMOutputDelta {
		t.Fatalf("first event type = %q, want output", bundle1.Events[0].EventType)
	}

	if !hub.flushTick() {
		t.Fatalf("second flushTick = false, want second bundle")
	}
	bundleEvent2 := receiveEvent(t, sub.Events())
	bundle2, err := DecodeEventBatch(bundleEvent2.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch 2: %v", err)
	}

	if len(bundle2.Events) != 1 {
		t.Fatalf("second bundle events = %d, want 1", len(bundle2.Events))
	}
	if bundle2.Events[0].EventType != EventLLMReasoningDelta {
		t.Fatalf("second event type = %q, want reasoning", bundle2.Events[0].EventType)
	}
}

// TestMergeFromDefaultBehaviorUnchanged verifies that mergeFrom without config
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
	merge, err := parent.mergeFrom(t.Context(), child, Filter{})
	if err != nil {
		t.Fatalf("mergeFrom: %v", err)
	}
	defer merge.Stop()

	// Emit a direct event (not bundled).
	if err := child.Emit(t.Context(), Event{
		EventType: EventAgentExecuteStarted,
		From:      Source{Layer: "agent", ID: "node_1"},
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
	if event.EventType != EventAgentExecuteStarted {
		t.Fatalf("EventType = %q, want agent.execute.started", event.EventType)
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

	sub, err := parent.Subscribe(Filter{}, WithRawStream())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	config := mergeWindowConfig{
		TickDuration:           10 * time.Millisecond,
		PerUpstreamBufferDepth: defaultMergePerUpstreamBufferDepth,
	}
	merge, err := parent.mergeWithConfig(t.Context(), child, Filter{}, config)
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}
	defer merge.Stop()

	// Emit an event.
	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_1"},
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
	bundle, err := DecodeEventBatch(event.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch: %v", err)
	}

	if len(bundle.Events) != 1 {
		t.Fatalf("Events = %d, want 1", len(bundle.Events))
	}
	if bundle.Events[0].EventType != EventLLMOutputDelta {
		t.Fatalf("Nested EventType = %q, want llm.output.delta", bundle.Events[0].EventType)
	}
}

func TestMergeWithConfigHubModeSharesDownstreamHub(t *testing.T) {
	parent := New(Scope{RequestID: "req_1"}, DefaultConfig())
	childA := New(Scope{NodeID: "node_a"}, DefaultConfig())
	childB := New(Scope{NodeID: "node_b"}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(childA.Close)
	t.Cleanup(childB.Close)

	sub, err := parent.Subscribe(Filter{EventTypes: []string{EventMergeBundle}}, WithRawStream())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	config := mergeWindowConfig{TickDuration: time.Hour}
	mergeA, err := parent.mergeWithConfig(t.Context(), childA, Filter{}, config)
	if err != nil {
		t.Fatalf("mergeWithConfig childA: %v", err)
	}
	defer mergeA.Stop()
	mergeB, err := parent.mergeWithConfig(t.Context(), childB, Filter{}, config)
	if err != nil {
		t.Fatalf("mergeWithConfig childB: %v", err)
	}
	defer mergeB.Stop()

	if mergeA.hub == nil || mergeA.hub != mergeB.hub {
		t.Fatalf("hub pointers = %p, %p; want shared downstream hub", mergeA.hub, mergeB.hub)
	}

	emitHubTestEvent(t, childA, "node_a", EventLLMOutputDelta, `"a1"`)
	emitHubTestEvent(t, childB, "node_b", EventLLMReasoningDelta, `"b1"`)
	waitForHubQueuedEventCount(t, mergeA.hub, 2)

	if !mergeA.hub.flushTick() {
		t.Fatalf("flushTick = false, want bundle")
	}
	bundleEvent := receiveEvent(t, sub.Events())
	bundle, err := DecodeEventBatch(bundleEvent.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch: %v", err)
	}
	if bundle.TickID != 1 {
		t.Fatalf("first tick ID = %d, want 1", bundle.TickID)
	}
	assertBundleNodes(t, bundle, "node_a", "node_b")
	assertNoEvent(t, sub.Events())

	emitHubTestEvent(t, childA, "node_a", EventLLMOutputDelta, `"a2"`)
	emitHubTestEvent(t, childB, "node_b", EventLLMReasoningDelta, `"b2"`)
	waitForHubQueuedEventCount(t, mergeA.hub, 2)
	if !mergeA.hub.flushTick() {
		t.Fatalf("second flushTick = false, want bundle")
	}
	bundleEvent = receiveEvent(t, sub.Events())
	bundle, err = DecodeEventBatch(bundleEvent.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch second: %v", err)
	}
	if bundle.TickID != 2 {
		t.Fatalf("second tick ID = %d, want 2", bundle.TickID)
	}
	assertBundleNodes(t, bundle, "node_a", "node_b")
}

func TestMergeWithConfigStopUnregistersOnlyOneSharedHubUpstream(t *testing.T) {
	parent := New(Scope{RequestID: "req_1"}, DefaultConfig())
	childA := New(Scope{NodeID: "node_a"}, DefaultConfig())
	childB := New(Scope{NodeID: "node_b"}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(childA.Close)
	t.Cleanup(childB.Close)

	sub, err := parent.Subscribe(Filter{EventTypes: []string{EventMergeBundle}}, WithRawStream())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	config := mergeWindowConfig{TickDuration: time.Hour}
	mergeA, err := parent.mergeWithConfig(t.Context(), childA, Filter{}, config)
	if err != nil {
		t.Fatalf("mergeWithConfig childA: %v", err)
	}
	mergeB, err := parent.mergeWithConfig(t.Context(), childB, Filter{}, config)
	if err != nil {
		t.Fatalf("mergeWithConfig childB: %v", err)
	}
	defer mergeB.Stop()

	emitHubTestEvent(t, childA, "node_a", EventLLMOutputDelta, `"drop me"`)
	waitForHubQueuedEventCount(t, mergeA.hub, 1)
	mergeA.Stop()

	select {
	case <-mergeA.Done():
	case <-time.After(time.Second):
		t.Fatalf("mergeA.Done() did not close after Stop")
	}
	if mergeA.hub.flushTick() {
		t.Fatalf("flushTick emitted stopped upstream event")
	}

	emitHubTestEvent(t, childB, "node_b", EventLLMOutputDelta, `"keep me"`)
	waitForHubQueuedEventCount(t, mergeB.hub, 1)
	if !mergeB.hub.flushTick() {
		t.Fatalf("flushTick after stopping mergeA = false, want mergeB bundle")
	}
	bundleEvent := receiveEvent(t, sub.Events())
	bundle, err := DecodeEventBatch(bundleEvent.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch: %v", err)
	}
	assertBundleNodes(t, bundle, "node_b")

	select {
	case <-mergeB.Done():
		t.Fatalf("mergeB.Done() closed after stopping mergeA")
	default:
	}
}

// TestMergeFromWithConfigHubRespectsFilters verifies that hub mode respects
// filters applied to upstream subscriptions.
func TestMergeFromWithConfigHubRespectsFilters(t *testing.T) {
	parent := New(Scope{RequestID: "req_1"}, DefaultConfig())
	child := New(Scope{NodeID: "node_1"}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(child.Close)

	sub, err := parent.Subscribe(Filter{}, WithRawStream())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	config := mergeWindowConfig{
		TickDuration:           10 * time.Millisecond,
		PerUpstreamBufferDepth: defaultMergePerUpstreamBufferDepth,
	}

	// Filter to only LLM events.
	filter := Filter{Prefixes: []string{"llm"}}
	merge, err := parent.mergeWithConfig(t.Context(), child, filter, config)
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}
	defer merge.Stop()

	// Emit two events: one LLM (should pass), one agent (should be filtered out).
	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"llm_delta"`),
	}); err != nil {
		t.Fatalf("Emit llm: %v", err)
	}

	if err := child.Emit(t.Context(), Event{
		EventType: EventAgentExecuteStarted,
		From:      Source{Layer: "agent", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("Emit agent: %v", err)
	}

	// Should receive only the LLM event in a bundle (agent is filtered).
	event, ok, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !ok {
		t.Fatalf("Next = not ok")
	}

	bundle, err := DecodeEventBatch(event.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch: %v", err)
	}

	if len(bundle.Events) != 1 {
		t.Fatalf("Events = %d, want 1 (agent filtered out)", len(bundle.Events))
	}
	if bundle.Events[0].EventType != EventLLMOutputDelta {
		t.Fatalf("EventType = %q, want llm.output.delta", bundle.Events[0].EventType)
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

	config := mergeWindowConfig{
		TickDuration:           10 * time.Millisecond,
		PerUpstreamBufferDepth: defaultMergePerUpstreamBufferDepth,
	}
	merge, err := parent.mergeWithConfig(ctx, child, Filter{}, config)
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}

	// Emit an event to ensure hub is running.
	if err := child.Emit(ctx, Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_1"},
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

	config := mergeWindowConfig{
		TickDuration:           10 * time.Millisecond,
		PerUpstreamBufferDepth: defaultMergePerUpstreamBufferDepth,
	}
	merge, err := parent.mergeWithConfig(t.Context(), child, Filter{}, config)
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

	_, err := parent.mergeWithConfig(t.Context(), child, Filter{}, mergeWindowConfig{
		TickDuration: -time.Millisecond,
	})
	if !errors.Is(err, ErrInvalidMerge) {
		t.Fatalf("MergeFromWithConfig negative tick err = %v, want ErrInvalidMerge", err)
	}
}

func TestDefaultHubMergeWindowConfigEnablesHubMode(t *testing.T) {
	config := defaultHubMergeWindowConfig()
	if config.TickDuration != defaultMergeTickDuration {
		t.Fatalf("TickDuration = %v, want %v", config.TickDuration, defaultMergeTickDuration)
	}
	if config.TickDuration <= 0 {
		t.Fatalf("TickDuration = %v, want hub mode enabled", config.TickDuration)
	}
	if config.PerUpstreamBufferDepth != defaultMergeWindowConfig().PerUpstreamBufferDepth {
		t.Fatalf("PerUpstreamBufferDepth = %d, want default depth", config.PerUpstreamBufferDepth)
	}
}

func TestMergeFromWithConfigHubEmptyUpstreamCloseStopsMerge(t *testing.T) {
	parent := New(Scope{}, DefaultConfig())
	child := New(Scope{}, DefaultConfig())
	t.Cleanup(parent.Close)

	merge, err := parent.mergeWithConfig(t.Context(), child, Filter{}, mergeWindowConfig{
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

	sub, err := parent.Subscribe(Filter{EventTypes: []string{EventMergeBundle}}, WithRawStream())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	merge, err := parent.mergeWithConfig(t.Context(), child, Filter{}, mergeWindowConfig{
		TickDuration: time.Hour,
	})
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}

	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"output"`),
	}); err != nil {
		t.Fatalf("Emit output: %v", err)
	}
	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMReasoningDelta,
		From:      Source{Layer: "agent", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"reasoning"`),
	}); err != nil {
		t.Fatalf("Emit reasoning: %v", err)
	}
	child.Close()

	first := receiveEvent(t, sub.Events())
	second := receiveEvent(t, sub.Events())
	firstBundle, err := DecodeEventBatch(first.Delta)
	if err != nil {
		t.Fatalf("Decode first bundle: %v", err)
	}
	secondBundle, err := DecodeEventBatch(second.Delta)
	if err != nil {
		t.Fatalf("Decode second bundle: %v", err)
	}
	if len(firstBundle.Events) != 1 || firstBundle.Events[0].EventType != EventLLMOutputDelta {
		t.Fatalf("first drained bundle = %+v, want output", firstBundle.Events)
	}
	if len(secondBundle.Events) != 1 || secondBundle.Events[0].EventType != EventLLMReasoningDelta {
		t.Fatalf("second drained bundle = %+v, want reasoning", secondBundle.Events)
	}

	select {
	case <-merge.Done():
	case <-time.After(time.Second):
		t.Fatalf("merge.Done() did not close after draining upstream")
	}
}

func TestMergeFromWithConfigHubEventsUseMergedScopeAndMetadata(t *testing.T) {
	parent := New(Scope{RequestID: "req_1"}, DefaultConfig())
	child := New(Scope{NodeID: "node_1"}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(child.Close)

	sub, err := parent.Subscribe(Filter{EventTypes: []string{EventMergeBundle}}, WithRawStream())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	merge, err := parent.mergeWithConfig(t.Context(), child, Filter{}, mergeWindowConfig{
		TickDuration: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}
	defer merge.Stop()

	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"output"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	bundleEvent := receiveEvent(t, sub.Events())
	bundle, err := DecodeEventBatch(bundleEvent.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch: %v", err)
	}
	if len(bundle.Events) != 1 {
		t.Fatalf("Events = %d, want 1", len(bundle.Events))
	}
	nested := bundle.Events[0]
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

	merge, err := parent.mergeWithConfig(t.Context(), child, Filter{}, mergeWindowConfig{
		TickDuration: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("MergeFromWithConfig: %v", err)
	}
	defer merge.Stop()

	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "agent", ID: "node_1"},
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

func emitHubTestEvent(t *testing.T, s *Stream, nodeID string, eventType string, delta string) {
	t.Helper()
	if err := s.Emit(t.Context(), Event{
		EventType: eventType,
		From:      Source{Layer: "agent", ID: nodeID},
		Status:    StatusRunning,
		Delta:     json.RawMessage(delta),
	}); err != nil {
		t.Fatalf("Emit %s: %v", nodeID, err)
	}
}

func waitForHubQueuedEventCount(t *testing.T, hub *mergeHub, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		hub.mu.RLock()
		got := 0
		for _, fifo := range hub.fifosByIdentity {
			got += fifo.len()
		}
		hub.mu.RUnlock()
		if got >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("queued hub events = %d, want at least %d", got, want)
		case <-ticker.C:
		}
	}
}

func assertBundleNodes(t *testing.T, bundle EventBatch, wantNodeIDs ...string) {
	t.Helper()
	if len(bundle.Events) != len(wantNodeIDs) {
		t.Fatalf("events = %d, want %d", len(bundle.Events), len(wantNodeIDs))
	}
	got := make(map[string]bool, len(bundle.Events))
	for _, event := range bundle.Events {
		got[event.Scope.NodeID] = true
	}
	for _, nodeID := range wantNodeIDs {
		if !got[nodeID] {
			t.Fatalf("bundle nodes = %v, missing %s", got, nodeID)
		}
	}
}
