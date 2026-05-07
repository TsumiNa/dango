package stream

import (
	"encoding/json"
	"fmt"
)

// EventMergeBundle is the event type name for merge bundle events.
// A merge bundle contains multiple nested stream events emitted within a single merge tick window.
const EventMergeBundle = "merge.bundle"

// EventBatch represents a collection of nested stream events emitted during
// one merge tick window. It includes the tick window metadata and the nested events
// that became ready for delivery during that tick.
//
// A bundle is the intermediate format used by the merge hub to group upstream
// deltas by tick window. Downstream consumers parse the bundle and select nested
// events they care about, typically by filtering on event type, source layer, or scope.
type EventBatch struct {
	// TickID is the logical tick identifier for this bundle window.
	// It can be used for debugging and reconstructing the merge order.
	TickID uint64 `json:"tick_id"`

	// Events is the list of stream events ready for delivery in this tick.
	// The order within Events matches the per-upstream FIFO order.
	Events []Event `json:"events"`
}

// UnmarshalJSON handles backward-compatible decoding for EventBatch payloads.
// It prioritizes the modern "events" field but falls back to decoding legacy
// payloads that used "nested_events".
func (b *EventBatch) UnmarshalJSON(data []byte) error {
	type rawBatch struct {
		TickID       uint64           `json:"tick_id"`
		Events       *json.RawMessage `json:"events"`
		NestedEvents []Event          `json:"nested_events"`
	}

	var raw rawBatch
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	b.TickID = raw.TickID
	if raw.Events != nil {
		if err := json.Unmarshal(*raw.Events, &b.Events); err != nil {
			return err
		}
	} else {
		b.Events = raw.NestedEvents
	}

	return nil
}

// EncodeEventBatch serializes an EventBatch into JSON-encoded raw message form
// suitable for use as an Event.Delta field. Returns an error if the bundle cannot
// be marshaled.
func EncodeEventBatch(bundle EventBatch) (json.RawMessage, error) {
	data, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("encode bundle: %w", err)
	}
	return json.RawMessage(data), nil
}

// DecodeEventBatch deserializes a JSON-encoded bundle delta into a EventBatch struct.
// Returns an error if the delta is not valid JSON or does not match the expected shape.
func DecodeEventBatch(delta json.RawMessage) (EventBatch, error) {
	var bundle EventBatch
	if err := json.Unmarshal(delta, &bundle); err != nil {
		return EventBatch{}, fmt.Errorf("decode bundle: %w", err)
	}
	return bundle, nil
}

// IsValidEventBatch reports whether a batch is valid for emission.
// An empty batch (no events) is not valid and should not be emitted.
func IsValidEventBatch(bundle EventBatch) bool {
	return len(bundle.Events) > 0
}

// ExpandBundleEvent returns the events a downstream consumer should handle for event.
// Merge bundle events expand to their nested events in bundle order. Non-bundle
// events pass through unchanged so callers can consume direct and bundled streams
// with one code path during migration. Expanded nested events inherit missing
// scope fields from the outer bundle event so scope filters behave like direct
// merge delivery.
func ExpandBundleEvent(event Event) ([]Event, error) {
	if event.EventType != EventMergeBundle {
		return []Event{event}, nil
	}

	bundle, err := DecodeEventBatch(event.Delta)
	if err != nil {
		return nil, fmt.Errorf("expand bundle event: %w", err)
	}
	if !IsValidEventBatch(bundle) {
		return nil, fmt.Errorf("expand bundle event: empty bundle")
	}

	events := make([]Event, len(bundle.Events))
	copy(events, bundle.Events)
	for i := range events {
		events[i].Scope = mergeScope(event.Scope, events[i].Scope)
	}
	return events, nil
}

// FilterBundleEvent expands event with [ExpandBundleEvent] and returns only the
// resulting events that match filter.
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
