package store

import (
	"context"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

// EventLogStore records and loads raw request stream event frames.
//
// Implementations belong to persistence packages such as internal/store/sqlite.
// They are not part of stream delivery; callers should use them from dedicated
// persistence goroutines so writes do not block normal subscribers.
type EventLogStore interface {
	// AppendEvent stores one prepared raw request-stream frame.
	AppendEvent(ctx context.Context, event streampkg.Event) error

	// LoadEvents returns matching raw frames in ascending sequence order.
	//
	// The returned events belong to the persisted request scope identified by
	// scope. When from is 0, implementations should load from the beginning of
	// the stored event log.
	LoadEvents(ctx context.Context, scope streampkg.Scope, from uint64, filter streampkg.Filter) ([]streampkg.Event, error)
}
