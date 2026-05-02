package stream

import "context"

// Store persists stream events and can later replay a filtered event range.
//
// Load returns matching events for the stream scope in ascending sequence
// order. Conversation session stores should only connect here later if they
// subscribe to a narrow lifecycle-state event family rather than the whole
// outward request stream.
type Store interface {
	Append(ctx context.Context, event Event) error
	Load(ctx context.Context, scope Scope, from uint64, filter Filter) ([]Event, error)
}
