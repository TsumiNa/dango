package stream

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEncodeEventBatchRoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 7, 10, 30, 0, 0, time.UTC)

	// Create nested events to include in the bundle
	nestedEvent1 := Event{
		EventType:      EventLLMOutputDelta,
		From:           Source{Layer: "skill", ID: "skill_1"},
		SequenceNumber: 1,
		Status:         StatusRunning,
		Delta:          json.RawMessage(`{"text":"hello"}`),
		Timestamp:      now,
		Scope:          Scope{RequestID: "req_1", RunnerID: "run_1"},
		Metadata:       map[string]any{"key": "value"},
	}

	nestedEvent2 := Event{
		EventType:      EventStatusProgress,
		From:           Source{Layer: "orchestrator"},
		SequenceNumber: 2,
		Status:         StatusRunning,
		Delta:          json.RawMessage(`{"progress":50}`),
		Timestamp:      now.Add(1 * time.Second),
		Scope:          Scope{RequestID: "req_1"},
		Metadata:       map[string]any{"stage": "processing"},
	}

	bundle := EventBatch{
		TickID: 42,
		Events: []Event{nestedEvent1, nestedEvent2},
	}

	// Encode the bundle
	encoded, err := EncodeEventBatch(bundle)
	if err != nil {
		t.Fatalf("EncodeEventBatch: %v", err)
	}

	// Verify it's valid JSON
	if !json.Valid(encoded) {
		t.Fatalf("encoded bundle is not valid JSON: %s", encoded)
	}

	// Decode it back
	decoded, err := DecodeEventBatch(encoded)
	if err != nil {
		t.Fatalf("DecodeEventBatch: %v", err)
	}

	// Verify the decoded bundle matches the original
	if decoded.TickID != bundle.TickID {
		t.Errorf("tick_id = %d, want %d", decoded.TickID, bundle.TickID)
	}

	if len(decoded.Events) != len(bundle.Events) {
		t.Fatalf("events count = %d, want %d", len(decoded.Events), len(bundle.Events))
	}

	// Check first nested event
	if decoded.Events[0].EventType != EventLLMOutputDelta {
		t.Errorf("nested event[0] event_type = %q, want %q", decoded.Events[0].EventType, EventLLMOutputDelta)
	}
	if decoded.Events[0].SequenceNumber != 1 {
		t.Errorf("nested event[0] sequence_number = %d, want 1", decoded.Events[0].SequenceNumber)
	}
	if decoded.Events[0].Status != StatusRunning {
		t.Errorf("nested event[0] status = %q, want %q", decoded.Events[0].Status, StatusRunning)
	}
	if !json.Valid(decoded.Events[0].Delta) {
		t.Errorf("nested event[0] delta is not valid JSON: %s", decoded.Events[0].Delta)
	}
	if decoded.Events[0].From.Layer != "skill" {
		t.Errorf("nested event[0] from.layer = %q, want skill", decoded.Events[0].From.Layer)
	}

	// Check second nested event
	if decoded.Events[1].EventType != EventStatusProgress {
		t.Errorf("nested event[1] event_type = %q, want %q", decoded.Events[1].EventType, EventStatusProgress)
	}
	if decoded.Events[1].SequenceNumber != 2 {
		t.Errorf("nested event[1] sequence_number = %d, want 2", decoded.Events[1].SequenceNumber)
	}
}

func TestEmptyEventBatchIsInvalid(t *testing.T) {
	bundle := EventBatch{
		TickID: 1,
		Events: []Event{},
	}

	if IsValidEventBatch(bundle) {
		t.Errorf("IsValidEventBatch(empty) = true, want false")
	}

	// Verify a non-empty bundle is valid
	bundle.Events = []Event{
		{
			EventType: EventStatusProgress,
			From:      Source{Layer: "test"},
			Status:    StatusRunning,
			Delta:     json.RawMessage("null"),
		},
	}

	if !IsValidEventBatch(bundle) {
		t.Errorf("IsValidEventBatch(non-empty) = false, want true")
	}
}

func TestEventBatchWithNestedFieldsPreservesMetadata(t *testing.T) {
	now := time.Date(2026, 5, 7, 10, 30, 0, 0, time.UTC)

	nestedEvent := Event{
		EventType:      EventStatusProgress,
		From:           Source{Layer: "orchestrator", ID: "orch_1", ParentID: "parent_orch"},
		SequenceNumber: 10,
		Status:         StatusRunning,
		Delta:          json.RawMessage(`{"detail":"test"}`),
		Timestamp:      now,
		Scope: Scope{
			RequestID: "req_123",
			RunnerID:  "run_456",
			NodeID:    "node_789",
			SessionID: "sess_abc",
		},
		Metadata: map[string]any{
			"custom_key": "custom_value",
			"nested_obj": map[string]any{"a": 1, "b": 2},
		},
	}

	bundle := EventBatch{
		TickID: 99,
		Events: []Event{nestedEvent},
	}

	encoded, err := EncodeEventBatch(bundle)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := DecodeEventBatch(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	ev := decoded.Events[0]
	if ev.From.ParentID != "parent_orch" {
		t.Errorf("from.parent_id = %q, want parent_orch", ev.From.ParentID)
	}
	if ev.Scope.RequestID != "req_123" {
		t.Errorf("scope.request_id = %q, want req_123", ev.Scope.RequestID)
	}
	if ev.Scope.NodeID != "node_789" {
		t.Errorf("scope.node_id = %q, want node_789", ev.Scope.NodeID)
	}
	if ev.Scope.SessionID != "sess_abc" {
		t.Errorf("scope.session_id = %q, want sess_abc", ev.Scope.SessionID)
	}

	if val, ok := ev.Metadata["custom_key"].(string); !ok || val != "custom_value" {
		t.Errorf("metadata custom_key = %v, want custom_value", ev.Metadata["custom_key"])
	}

	if nested, ok := ev.Metadata["nested_obj"].(map[string]any); !ok || nested["a"] != float64(1) {
		t.Errorf("metadata nested_obj.a = %v, want 1", nested["a"])
	}
}

func TestDecodeEventBatchWithInvalidJSON(t *testing.T) {
	invalidDelta := json.RawMessage(`{not valid json}`)

	_, err := DecodeEventBatch(invalidDelta)
	if err == nil {
		t.Errorf("DecodeEventBatch with invalid JSON = nil, want error")
	}
}

func TestBundleEventTypeConstantExists(t *testing.T) {
	if EventMergeBundle != "merge.bundle" {
		t.Errorf("EventMergeBundle = %q, want merge.bundle", EventMergeBundle)
	}
}

func TestExpandBundleEventReturnsEventsInOrder(t *testing.T) {
	first := Event{
		EventType: EventLLMReasoningDelta,
		From:      Source{Layer: "skill", ID: "skill_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"thinking"`),
	}
	second := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "skill", ID: "skill_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"answer"`),
	}
	delta, err := EncodeEventBatch(EventBatch{
		TickID: 7,
		Events: []Event{first, second},
	})
	if err != nil {
		t.Fatalf("EncodeEventBatch: %v", err)
	}

	events, err := ExpandBundleEvent(Event{
		EventType: EventMergeBundle,
		From:      Source{Layer: "hub"},
		Status:    StatusCompleted,
		Delta:     delta,
	})
	if err != nil {
		t.Fatalf("ExpandBundleEvent: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expanded events = %d, want 2", len(events))
	}
	if events[0].EventType != EventLLMReasoningDelta || events[1].EventType != EventLLMOutputDelta {
		t.Fatalf("expanded order = %q, %q", events[0].EventType, events[1].EventType)
	}
}

func TestFilterBundleEventSelectsEvents(t *testing.T) {
	visible := Event{
		EventType: EventLLMOutputDelta,
		From:      Source{Layer: "skill", ID: "skill_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`"visible"`),
	}
	hidden := Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "executor", ID: "node_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`{"progress":50}`),
	}
	delta, err := EncodeEventBatch(EventBatch{
		TickID: 8,
		Events: []Event{hidden, visible},
	})
	if err != nil {
		t.Fatalf("EncodeEventBatch: %v", err)
	}

	events, err := FilterBundleEvent(Event{
		EventType: EventMergeBundle,
		From:      Source{Layer: "hub"},
		Status:    StatusCompleted,
		Delta:     delta,
	}, Filter{Prefixes: []string{"llm."}})
	if err != nil {
		t.Fatalf("FilterBundleEvent: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("filtered events = %d, want 1", len(events))
	}
	if events[0].EventType != EventLLMOutputDelta {
		t.Fatalf("filtered event type = %q, want %q", events[0].EventType, EventLLMOutputDelta)
	}
}

func TestFilterBundleEventMergesBundleScopeBeforeMatching(t *testing.T) {
	delta, err := EncodeEventBatch(EventBatch{
		TickID: 9,
		Events: []Event{{
			EventType: EventRunnerNodeCompleted,
			From:      Source{Layer: "runner", ID: "runner_1"},
			Status:    StatusCompleted,
			Scope:     Scope{NodeID: "node_1"},
			Delta:     json.RawMessage(`{"event":"NodeCompleted"}`),
		}},
	})
	if err != nil {
		t.Fatalf("EncodeEventBatch: %v", err)
	}

	events, err := FilterBundleEvent(Event{
		EventType: EventMergeBundle,
		From:      Source{Layer: "hub"},
		Status:    StatusCompleted,
		Scope:     Scope{RequestID: "request_1", RunnerID: "runner_1"},
		Delta:     delta,
	}, Filter{Scope: Scope{RequestID: "request_1", RunnerID: "runner_1", NodeID: "node_1"}})
	if err != nil {
		t.Fatalf("FilterBundleEvent: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("filtered events = %d, want 1", len(events))
	}
	if events[0].Scope.RequestID != "request_1" || events[0].Scope.RunnerID != "runner_1" || events[0].Scope.NodeID != "node_1" {
		t.Fatalf("expanded scope = %+v, want request/runner from bundle and node from nested", events[0].Scope)
	}
}

func TestExpandBundleEventPassesThroughNonBundleEvents(t *testing.T) {
	event := Event{
		EventType: EventStatusProgress,
		From:      Source{Layer: "runner", ID: "runner_1"},
		Status:    StatusRunning,
		Delta:     json.RawMessage(`{"progress":10}`),
	}

	events, err := ExpandBundleEvent(event)
	if err != nil {
		t.Fatalf("ExpandBundleEvent: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expanded events = %d, want 1", len(events))
	}
	if events[0].EventType != event.EventType || events[0].From.ID != event.From.ID {
		t.Fatalf("expanded event = %+v, want pass-through %+v", events[0], event)
	}

	filtered, err := FilterBundleEvent(event, Filter{EventTypes: []string{EventLLMOutputDelta}})
	if err != nil {
		t.Fatalf("FilterBundleEvent: %v", err)
	}
	if len(filtered) != 0 {
		t.Fatalf("filtered pass-through events = %d, want 0", len(filtered))
	}
}

func TestExpandBundleEventMalformedEventBatchReturnsError(t *testing.T) {
	delta := json.RawMessage(`{"tick_id":9,"events":`)
	_, err := ExpandBundleEvent(Event{
		EventType: EventMergeBundle,
		From:      Source{Layer: "hub"},
		Status:    StatusCompleted,
		Delta:     delta,
	})
	if err == nil {
		t.Fatalf("ExpandBundleEvent malformed payload error = nil, want error")
	}
}

func TestDecodeEventBatchSupportsLegacyNestedEvents(t *testing.T) {
	legacyJSON := json.RawMessage(`{
		"tick_id": 42,
		"nested_events": [
			{
				"event_type": "llm.output.delta",
				"from": {"layer": "skill", "id": "skill_1"}
			}
		]
	}`)

	bundle, err := DecodeEventBatch(legacyJSON)
	if err != nil {
		t.Fatalf("DecodeEventBatch(legacyJSON): %v", err)
	}

	if bundle.TickID != 42 {
		t.Errorf("TickID = %d, want 42", bundle.TickID)
	}
	if len(bundle.Events) != 1 {
		t.Fatalf("Events count = %d, want 1", len(bundle.Events))
	}
	if bundle.Events[0].EventType != EventLLMOutputDelta {
		t.Errorf("Events[0].EventType = %q, want %q", bundle.Events[0].EventType, EventLLMOutputDelta)
	}
}
