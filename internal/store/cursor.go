package store

import (
	"context"
	"errors"
	"time"
)

// ErrSnapshotCursorNotFound is returned when a request has no stored cursor.
var ErrSnapshotCursorNotFound = errors.New("store: snapshot cursor not found")

// SnapshotCursor records the latest persisted replay position for one request.
//
// EventSequence tracks the last top-level request-stream frame that has been
// materialized into a describe view. CheckpointSequence tracks the latest
// runner checkpoint sequence the describer incorporated when building that
// view.
type SnapshotCursor struct {
	RequestID          string
	RunnerID           string
	CheckpointSequence int64
	EventSequence      uint64
	UpdatedAt          time.Time
}

// SnapshotCursorStore persists per-request describe replay cursors.
type SnapshotCursorStore interface {
	// SaveCursor inserts or replaces the cursor for cursor.RequestID.
	SaveCursor(ctx context.Context, cursor SnapshotCursor) error

	// LoadCursor returns the stored cursor for requestID.
	LoadCursor(ctx context.Context, requestID string) (SnapshotCursor, error)
}
