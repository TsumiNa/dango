package stream

import (
	"encoding/json"
	"fmt"
)

// EventMergeBundle is the event type name for merge bundle events.
// A merge bundle contains multiple nested stream events emitted within a single merge tick window.
const EventMergeBundle = "merge.bundle"

// BundlePayload represents a collection of nested stream events emitted during
// one merge tick window. It includes the tick window metadata and the nested events
// that became ready for delivery during that tick.
//
// A bundle is the intermediate format used by the merge hub to group upstream
// deltas by tick window. Downstream consumers parse the bundle and select nested
// events they care about, typically by filtering on event type, source layer, or scope.
type BundlePayload struct {
	// TickID is the logical tick identifier for this bundle window.
	// It can be used for debugging and reconstructing the merge order.
	TickID uint64 `json:"tick_id"`

	// NestedEvents is the list of stream events ready for delivery in this tick.
	// The order within NestedEvents matches the per-upstream FIFO order.
	NestedEvents []Event `json:"nested_events"`
}

// EncodeBundlePayload serializes a BundlePayload into JSON-encoded raw message form
// suitable for use as an Event.Delta field. Returns an error if the bundle cannot
// be marshaled.
func EncodeBundlePayload(bundle BundlePayload) (json.RawMessage, error) {
	data, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("encode bundle: %w", err)
	}
	return json.RawMessage(data), nil
}

// DecodeBundlePayload deserializes a JSON-encoded bundle delta into a BundlePayload struct.
// Returns an error if the delta is not valid JSON or does not match the expected shape.
func DecodeBundlePayload(delta json.RawMessage) (BundlePayload, error) {
	var bundle BundlePayload
	if err := json.Unmarshal(delta, &bundle); err != nil {
		return BundlePayload{}, fmt.Errorf("decode bundle: %w", err)
	}
	return bundle, nil
}

// IsValidBundlePayload reports whether a bundle is valid for emission.
// An empty bundle (no nested events) is not valid and should not be emitted.
func IsValidBundlePayload(bundle BundlePayload) bool {
	return len(bundle.NestedEvents) > 0
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

	bundle, err := DecodeBundlePayload(event.Delta)
	if err != nil {
		return nil, fmt.Errorf("expand bundle event: %w", err)
	}
	if !IsValidBundlePayload(bundle) {
		return nil, fmt.Errorf("expand bundle event: empty bundle")
	}

	events := make([]Event, len(bundle.NestedEvents))
	copy(events, bundle.NestedEvents)
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
