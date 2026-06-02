package stream

import (
	"encoding/json"
	"fmt"
)

// EventMergeBundle is the event type name for merge bundle events.
// A merge bundle contains multiple nested stream events emitted within a single merge tick window.
const EventMergeBundle = "merge.bundle"

// EventBatch represents a collection of stream events emitted during one merge
// tick window. It includes the tick window metadata and the events that became
// ready for delivery during that tick.
//
// An EventBatch is the intermediate format used by the merge hub to group
// upstream deltas by tick window. Downstream consumers parse the batch and
// select events they care about, typically by filtering on event type, source
// layer, or scope.
type EventBatch struct {
	// TickID is the logical tick identifier for this bundle window.
	// It can be used for debugging and reconstructing the merge order.
	TickID uint64 `json:"tick_id"`

	// Events is the list of stream events ready for delivery in this tick.
	// The order within Events matches the per-upstream FIFO order.
	Events []Event `json:"events"`
}

// EncodeEventBatch serializes an EventBatch into JSON-encoded raw message form
// suitable for use as an Event.Delta field. Returns an error if the batch cannot
// be marshaled.
func EncodeEventBatch(bundle EventBatch) (json.RawMessage, error) {
	data, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("encode event batch: %w", err)
	}
	return json.RawMessage(data), nil
}

// DecodeEventBatch deserializes a JSON-encoded batch delta into an EventBatch struct.
// Returns an error if the delta is not valid JSON or does not match the expected shape.
func DecodeEventBatch(delta json.RawMessage) (EventBatch, error) {
	var bundle EventBatch
	if err := json.Unmarshal(delta, &bundle); err != nil {
		return EventBatch{}, fmt.Errorf("decode event batch: %w", err)
	}
	return bundle, nil
}

// isValidEventBatch reports whether a batch is valid for emission.
// An empty batch (no events) is not valid and should not be emitted.
func isValidEventBatch(bundle EventBatch) bool {
	return len(bundle.Events) > 0
}

// ExpandBundleEvent expands a raw merge bundle event carrier into its nested
// logical events. It is intended for raw stream consumers that receive
// merge.bundle frames via [WithRawStream] and need to inspect or iterate the
// contained events. Non-bundle events pass through unchanged so the same code
// path can handle both plain and bundle frames when working with raw streams.
// Expanded nested events inherit missing scope fields from the outer bundle
// event so that scope comparisons behave consistently with direct delivery.
//
// Ordinary [Stream.Subscribe] and [Stream.Replay] callers do not need this
// function; they receive already-expanded logical events by default.
func ExpandBundleEvent(event Event) ([]Event, error) {
	if event.EventType != EventMergeBundle {
		return []Event{event}, nil
	}

	bundle, err := DecodeEventBatch(event.Delta)
	if err != nil {
		return nil, fmt.Errorf("expand bundle event: %w", err)
	}
	if !isValidEventBatch(bundle) {
		return nil, fmt.Errorf("expand bundle event: empty event batch")
	}

	events := make([]Event, len(bundle.Events))
	copy(events, bundle.Events)
	for i := range events {
		events[i].Scope = mergeScope(event.Scope, events[i].Scope)
	}
	return events, nil
}

// FilterBundleEvent expands a raw bundle event with [ExpandBundleEvent] and
// returns only the events that match filter. Like [ExpandBundleEvent], this
// function is for raw stream consumers that opt into merge.bundle frame
// delivery via [WithRawStream]. Ordinary subscribers do not need it.
func FilterBundleEvent(event Event, filter Filter) ([]Event, error) {
	events, err := ExpandBundleEvent(event)
	if err != nil {
		return nil, err
	}

	filtered := make([]Event, 0, len(events))
	for _, candidate := range events {
		if filter.Match(candidate) {
			filtered = append(filtered, candidate)
		}
	}
	return filtered, nil
}
