package stream

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// makeTestEvent constructs a minimal Event suitable for frame normalization tests.
func makeTestEvent(eventType, layer, id string, seq uint64, status string) Event {
	return Event{
		EventType:      eventType,
		From:           Source{Layer: layer, ID: id},
		SequenceNumber: seq,
		Status:         status,
		Delta:          json.RawMessage(`"hello"`),
		Timestamp:      time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC),
		Scope:          Scope{RequestID: "req_1", NodeID: id},
		Metadata:       map[string]any{"custom": "value"},
	}
}

func TestNormalizeDirectFrameCreatesOneEventFrameWithNoTickID(t *testing.T) {
	event := makeTestEvent(EventLLMOutputDelta, "skill", "sk_1", 7, StatusRunning)

	frame := normalizeDirectFrame(event)

	if frame.TickID != 0 {
		t.Errorf("direct frame TickID = %d, want 0", frame.TickID)
	}
	if len(frame.Events) != 1 {
		t.Fatalf("direct frame Events len = %d, want 1", len(frame.Events))
	}
}

func TestNormalizeDirectFramePreservesEventFields(t *testing.T) {
	event := makeTestEvent(EventLLMOutputDelta, "skill", "sk_1", 7, StatusRunning)

	frame := normalizeDirectFrame(event)
	prepared := frame.Events[0]

	// Source, EventType, Status, Scope, Delta, Timestamp must all be preserved.
	if prepared.EventType != EventLLMOutputDelta {
		t.Errorf("EventType = %q, want %q", prepared.EventType, EventLLMOutputDelta)
	}
	if prepared.From.Layer != "skill" {
		t.Errorf("From.Layer = %q, want skill", prepared.From.Layer)
	}
	if prepared.From.ID != "sk_1" {
		t.Errorf("From.ID = %q, want sk_1", prepared.From.ID)
	}
	if prepared.Status != StatusRunning {
		t.Errorf("Status = %q, want %q", prepared.Status, StatusRunning)
	}
	if string(prepared.Delta) != `"hello"` {
		t.Errorf("Delta = %s, want \"hello\"", prepared.Delta)
	}
	if prepared.Scope.RequestID != "req_1" {
		t.Errorf("Scope.RequestID = %q, want req_1", prepared.Scope.RequestID)
	}
	if prepared.Scope.NodeID != "sk_1" {
		t.Errorf("Scope.NodeID = %q, want sk_1", prepared.Scope.NodeID)
	}
	if !prepared.Timestamp.Equal(event.Timestamp) {
		t.Errorf("Timestamp = %v, want %v", prepared.Timestamp, event.Timestamp)
	}
}

func TestNormalizeDirectFrameAddsUpstreamSequenceMetadata(t *testing.T) {
	event := makeTestEvent(EventLLMOutputDelta, "skill", "sk_1", 7, StatusRunning)

	frame := normalizeDirectFrame(event)
	prepared := frame.Events[0]

	// prepareMergedEvent must stamp upstream_sequence_number and zero out SequenceNumber.
	if prepared.SequenceNumber != 0 {
		t.Errorf("SequenceNumber = %d, want 0 (cleared by prepare)", prepared.SequenceNumber)
	}
	upstream, ok := prepared.Metadata["upstream_sequence_number"]
	if !ok {
		t.Fatalf("upstream_sequence_number missing from metadata")
	}
	if upstream != uint64(7) {
		t.Errorf("upstream_sequence_number = %v (%T), want uint64(7)", upstream, upstream)
	}
}

func TestNormalizeDirectFramePreservesExistingMetadata(t *testing.T) {
	event := makeTestEvent(EventLLMOutputDelta, "skill", "sk_1", 3, StatusRunning)

	frame := normalizeDirectFrame(event)
	prepared := frame.Events[0]

	if prepared.Metadata["custom"] != "value" {
		t.Errorf("custom metadata lost: got %v", prepared.Metadata["custom"])
	}
}

func TestNormalizeTickFrameSingleUpstream(t *testing.T) {
	event := makeTestEvent(EventStatusProgress, "executor", "ex_1", 2, StatusRunning)
	items := []Event{event}

	frame := normalizeTickFrame(items, 5)

	if frame.TickID != 5 {
		t.Errorf("TickID = %d, want 5", frame.TickID)
	}
	if len(frame.Events) != 1 {
		t.Fatalf("Events len = %d, want 1", len(frame.Events))
	}
	if frame.Events[0].EventType != EventStatusProgress {
		t.Errorf("EventType = %q, want %q", frame.Events[0].EventType, EventStatusProgress)
	}
}

func TestNormalizeTickFrameMultipleUpstreams(t *testing.T) {
	items := []Event{
		makeTestEvent(EventLLMOutputDelta, "skill", "sk_a", 1, StatusRunning),
		makeTestEvent(EventStatusProgress, "executor", "ex_b", 2, StatusRunning),
	}

	frame := normalizeTickFrame(items, 12)

	if frame.TickID != 12 {
		t.Errorf("TickID = %d, want 12", frame.TickID)
	}
	if len(frame.Events) != 2 {
		t.Fatalf("Events len = %d, want 2", len(frame.Events))
	}
}

func TestNormalizeTickFramePreservesEventOrder(t *testing.T) {
	a := makeTestEvent(EventLLMOutputDelta, "skill", "sk_a", 1, StatusRunning)
	b := makeTestEvent(EventStatusProgress, "executor", "ex_b", 2, StatusRunning)
	c := makeTestEvent(EventStatusCompleted, "orchestrator", "", 3, StatusCompleted)

	frame := normalizeTickFrame([]Event{a, b, c}, 1)

	if frame.Events[0].From.ID != "sk_a" {
		t.Errorf("Events[0] ID = %q, want sk_a", frame.Events[0].From.ID)
	}
	if frame.Events[1].From.ID != "ex_b" {
		t.Errorf("Events[1] ID = %q, want ex_b", frame.Events[1].From.ID)
	}
	if frame.Events[2].From.Layer != "orchestrator" {
		t.Errorf("Events[2] Layer = %q, want orchestrator", frame.Events[2].From.Layer)
	}
}

func TestEmitFrameDirectPathEmitsPlainEvent(t *testing.T) {
	downstream := New(Scope{RequestID: "req_direct"}, DefaultConfig())
	t.Cleanup(downstream.Close)

	sub, err := downstream.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	event := makeTestEvent(EventLLMOutputDelta, "skill", "sk_1", 3, StatusRunning)
	frame := normalizeDirectFrame(event)

	if err := emitFrame(t.Context(), downstream, frame); err != nil {
		t.Fatalf("emitFrame: %v", err)
	}

	received, ok, err := sub.Next(t.Context())
	if err != nil || !ok {
		t.Fatalf("Next = ok %v err %v", ok, err)
	}

	// Direct path: event is emitted as its own type, not as merge.bundle.
	if received.EventType != EventLLMOutputDelta {
		t.Errorf("EventType = %q, want %q", received.EventType, EventLLMOutputDelta)
	}
}

func TestEmitFrameHubPathEmitsBundleEvent(t *testing.T) {
	downstream := New(Scope{RequestID: "req_hub"}, DefaultConfig())
	t.Cleanup(downstream.Close)

	sub, err := downstream.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	items := []Event{
		makeTestEvent(EventLLMOutputDelta, "skill", "sk_a", 1, StatusRunning),
		makeTestEvent(EventStatusProgress, "executor", "ex_b", 2, StatusRunning),
	}
	frame := normalizeTickFrame(items, 1)

	if err := emitFrame(t.Context(), downstream, frame); err != nil {
		t.Fatalf("emitFrame: %v", err)
	}

	received, ok, err := sub.Next(t.Context())
	if err != nil || !ok {
		t.Fatalf("Next = ok %v err %v", ok, err)
	}

	// Hub path: emitted as merge.bundle.
	if received.EventType != EventMergeBundle {
		t.Errorf("EventType = %q, want %q", received.EventType, EventMergeBundle)
	}

	batch, err := DecodeEventBatch(received.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch: %v", err)
	}
	if batch.TickID != 1 {
		t.Errorf("TickID = %d, want 1", batch.TickID)
	}
	if len(batch.Events) != 2 {
		t.Fatalf("batch.Events len = %d, want 2", len(batch.Events))
	}
}

func TestEmitFrameSingleEventHubTickEmitsBundleNotPlain(t *testing.T) {
	// A single-event frame with a non-zero TickID is a hub tick (not direct),
	// so it must still be emitted as merge.bundle to preserve hub-mode observable
	// behavior.
	downstream := New(Scope{RequestID: "req_hub_single"}, DefaultConfig())
	t.Cleanup(downstream.Close)

	sub, err := downstream.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	items := []Event{makeTestEvent(EventStatusProgress, "executor", "ex_1", 1, StatusRunning)}
	frame := normalizeTickFrame(items, 3)

	if err := emitFrame(t.Context(), downstream, frame); err != nil {
		t.Fatalf("emitFrame: %v", err)
	}

	received, ok, err := sub.Next(t.Context())
	if err != nil || !ok {
		t.Fatalf("Next = ok %v err %v", ok, err)
	}

	if received.EventType != EventMergeBundle {
		t.Errorf("single-event hub tick EventType = %q, want %q", received.EventType, EventMergeBundle)
	}

	batch, err := DecodeEventBatch(received.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch: %v", err)
	}
	if batch.TickID != 3 {
		t.Errorf("TickID = %d, want 3", batch.TickID)
	}
}

func TestEmitFrameEmptyFrameEmitsNothing(t *testing.T) {
	downstream := New(Scope{}, DefaultConfig())
	t.Cleanup(downstream.Close)

	sub, err := downstream.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	frame := deliveryFrame{TickID: 0, Events: nil}
	if err := emitFrame(t.Context(), downstream, frame); err != nil {
		t.Fatalf("emitFrame empty: %v", err)
	}

	// No event should arrive.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, ok, _ := sub.Next(ctx)
	if ok {
		t.Error("emitFrame with empty frame should not emit any event")
	}
}

func TestRunDirectUsesFrameNormalization(t *testing.T) {
	// End-to-end: direct MergeFrom should forward events as plain events (not bundles).
	parent := New(Scope{RequestID: "req_direct_e2e"}, DefaultConfig())
	child := New(Scope{NodeID: "child_1"}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(child.Close)

	sub, err := parent.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe parent: %v", err)
	}
	merge, err := parent.MergeFrom(t.Context(), child, Filter{})
	if err != nil {
		t.Fatalf("MergeFrom: %v", err)
	}
	defer merge.Stop()

	if err := child.Emit(t.Context(), Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "skill", ID: "sk_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"chunk"`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	received, ok, err := sub.Next(t.Context())
	if err != nil || !ok {
		t.Fatalf("Next = ok %v err %v", ok, err)
	}
	// Direct path must not wrap in merge.bundle.
	if received.EventType != EventLLMOutputDelta {
		t.Errorf("EventType = %q, want %q (direct path must not bundle)", received.EventType, EventLLMOutputDelta)
	}
	// Upstream sequence metadata must be stamped.
	if received.Metadata["upstream_sequence_number"] == nil {
		t.Error("upstream_sequence_number missing from merged event")
	}
}

func TestFlushTickUsesFrameNormalization(t *testing.T) {
	// End-to-end: hub-mode MergeWithConfig should emit merge.bundle events.
	parent := New(Scope{RequestID: "req_hub_e2e"}, DefaultConfig())
	child := New(Scope{NodeID: "child_hub"}, DefaultConfig())
	t.Cleanup(parent.Close)
	t.Cleanup(child.Close)

	sub, err := parent.Subscribe(Filter{})
	if err != nil {
		t.Fatalf("Subscribe parent: %v", err)
	}
	cfg := MergeWindowConfig{
		TickDuration:           5 * time.Millisecond,
		PerUpstreamBufferDepth: 64,
	}
	merge, err := parent.MergeWithConfig(t.Context(), child, Filter{}, cfg)
	if err != nil {
		t.Fatalf("MergeWithConfig: %v", err)
	}
	defer merge.Stop()

	if err := child.Emit(t.Context(), Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "executor", ID: "ex_hub"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`{"stage":"run"}`),
	}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	received, ok, err := sub.Next(t.Context())
	if err != nil || !ok {
		t.Fatalf("Next = ok %v err %v", ok, err)
	}
	// Hub path must emit merge.bundle.
	if received.EventType != EventMergeBundle {
		t.Errorf("EventType = %q, want %q (hub tick must bundle)", received.EventType, EventMergeBundle)
	}
	batch, err := DecodeEventBatch(received.Delta)
	if err != nil {
		t.Fatalf("DecodeEventBatch: %v", err)
	}
	if len(batch.Events) != 1 {
		t.Fatalf("batch.Events len = %d, want 1", len(batch.Events))
	}
	// TickID must be set.
	if batch.TickID == 0 {
		t.Error("TickID = 0, want non-zero for hub tick")
	}
	// Upstream sequence metadata preserved in nested event.
	inner := batch.Events[0]
	if inner.Metadata["upstream_sequence_number"] == nil {
		t.Error("upstream_sequence_number missing from hub inner event")
	}
}
