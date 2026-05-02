package stream

import (
	"context"
	"sync"
)

// SubscribeOption customizes one subscription.
type SubscribeOption func(*subscribeSettings)

type subscribeSettings struct {
	replayFrom uint64
	replayLast int
	noReplay   bool
	buffer     int
}

// WithReplayFrom replays buffered events whose sequence number is at least
// sequence before live delivery starts.
func WithReplayFrom(sequence uint64) SubscribeOption {
	return func(settings *subscribeSettings) {
		settings.replayFrom = sequence
		settings.noReplay = false
	}
}

// WithReplayLast replays the last n buffered events before live delivery
// starts. The subscription filter still applies to replayed events.
func WithReplayLast(n int) SubscribeOption {
	return func(settings *subscribeSettings) {
		if n < 0 {
			n = 0
		}
		settings.replayLast = n
		settings.noReplay = false
	}
}

// WithNoReplay disables initial catch-up delivery.
func WithNoReplay() SubscribeOption {
	return func(settings *subscribeSettings) {
		settings.noReplay = true
	}
}

// WithSubscriberBuffer sets the channel buffer size for one subscription.
func WithSubscriberBuffer(n int) SubscribeOption {
	return func(settings *subscribeSettings) {
		settings.buffer = n
	}
}

// Subscription receives events from a stream until cancelled or until the
// stream closes.
type Subscription struct {
	id     uint64
	stream *Stream
	filter Filter
	events chan Event
	done   chan struct{}

	doneOnce   sync.Once
	eventsOnce sync.Once
}

// Events returns the delivery channel for this subscription.
func (sub *Subscription) Events() <-chan Event {
	return sub.events
}

// Next waits for the next event, returning ok=false when the subscription
// closes. Context cancellation is returned as an error so callers can use a
// simple iterator-style loop with explicit cancellation handling.
func (sub *Subscription) Next(ctx context.Context) (Event, bool, error) {
	if sub == nil {
		return Event{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case event, ok := <-sub.events:
		return event, ok, nil
	case <-ctx.Done():
		return Event{}, false, ctx.Err()
	}
}

// Cancel detaches the subscription and closes its event channel.
func (sub *Subscription) Cancel() {
	if sub == nil {
		return
	}
	sub.closeDone()

	sub.stream.deliveryMu.Lock()
	defer sub.stream.deliveryMu.Unlock()

	sub.stream.mu.Lock()
	delete(sub.stream.subscribers, sub.id)
	sub.stream.mu.Unlock()

	sub.closeEvents()
}

func (sub *Subscription) send(ctx context.Context, event Event) error {
	select {
	case sub.events <- event:
		return nil
	case <-sub.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (sub *Subscription) closeDone() {
	sub.doneOnce.Do(func() {
		close(sub.done)
	})
}

func (sub *Subscription) closeEvents() {
	sub.eventsOnce.Do(func() {
		close(sub.events)
	})
}
