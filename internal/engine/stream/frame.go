package stream

import (
	"context"
	"fmt"
)

// deliveryFrame is the internal transport unit for downstream stream delivery.
// It carries one or more logical events plus optional tick metadata.
//
// Direct-forwarded events produce a one-event frame with TickID == 0.
// Hub-mode tick flushes produce a frame with a positive TickID and one or more
// events ordered by per-upstream FIFO arrival.
type deliveryFrame struct {
	// TickID is the hub tick identifier for this frame. Zero means the frame
	// came from direct forwarding rather than a hub merge tick.
	TickID uint64

	// Events holds the logical events ready for downstream delivery.
	// Always contains at least one event for a valid frame.
	Events []Event
}

// normalizeDirectFrame creates a deliveryFrame for a single directly-forwarded
// event. The event is prepared with upstream sequence metadata before being
// placed in the frame.
func normalizeDirectFrame(event Event) deliveryFrame {
	return deliveryFrame{
		TickID: 0,
		Events: []Event{prepareMergedEvent(event)},
	}
}

// normalizeTickFrame creates a deliveryFrame for a hub-mode tick flush.
// items must already be prepared via prepareBundledMergedEvent before they
// reach this function (the hub enqueue step handles preparation).
// tickID must be the hub's current tick counter value for this flush.
func normalizeTickFrame(items []Event, tickID uint64) deliveryFrame {
	return deliveryFrame{
		TickID: tickID,
		Events: items,
	}
}

// emitFrame writes the delivery frame to the downstream stream.
//
// Single-event frames with TickID == 0 are emitted as plain events, preserving
// the direct-forwarding observable behavior for current subscribers.
// All other frames (hub ticks, or single-event hub ticks) are emitted as
// merge.bundle events containing an EventBatch payload, preserving the current
// hub-mode observable behavior.
//
// Returns nil if the frame contains no events.
func emitFrame(ctx context.Context, downstream *Stream, frame deliveryFrame) error {
	if len(frame.Events) == 0 {
		return nil
	}

	// Direct-forwarding path: single event, no tick window.
	if len(frame.Events) == 1 && frame.TickID == 0 {
		return downstream.Emit(ctx, frame.Events[0])
	}

	// Hub-mode path: wrap events in a merge.bundle event.
	bundle := EventBatch{
		TickID: frame.TickID,
		Events: frame.Events,
	}
	delta, err := EncodeEventBatch(bundle)
	if err != nil {
		return fmt.Errorf("emit frame: %w", err)
	}
	bundleEvent := Event{
		EventType: EventMergeBundle,
		From: Source{
			Layer: "hub",
		},
		Status: StatusCompleted,
		Delta:  delta,
	}
	return downstream.Emit(ctx, bundleEvent)
}
