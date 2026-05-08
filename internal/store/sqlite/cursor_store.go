package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	storepkg "github.com/tsumina/dango/internal/store"
	sqldb "github.com/tsumina/dango/internal/store/sqlite/db"
)

var _ storepkg.SnapshotCursorStore = (*SnapshotCursorStore)(nil)

// SnapshotCursorStore persists request-scoped describe replay cursors in
// SQLite.
type SnapshotCursorStore struct {
	store *Store
}

// NewSnapshotCursorStore returns a SQLite-backed store for snapshot cursors.
func NewSnapshotCursorStore(store *Store) *SnapshotCursorStore {
	return &SnapshotCursorStore{store: store}
}

// SaveCursor inserts or replaces the cursor for cursor.RequestID.
func (s *SnapshotCursorStore) SaveCursor(ctx context.Context, cursor storepkg.SnapshotCursor) error {
	if s == nil || s.store == nil || s.store.queries == nil {
		return fmt.Errorf("sqlite: SnapshotCursorStore.SaveCursor called on nil store")
	}
	if cursor.RequestID == "" {
		return fmt.Errorf("sqlite: snapshot cursor missing request_id")
	}
	if cursor.CheckpointSequence < 0 {
		return fmt.Errorf("sqlite: snapshot cursor checkpoint_sequence must be non-negative")
	}
	if cursor.EventSequence > math.MaxInt64 {
		return fmt.Errorf("sqlite: snapshot cursor event_sequence %d exceeds int64", cursor.EventSequence)
	}
	if cursor.UpdatedAt.IsZero() {
		cursor.UpdatedAt = time.Now().UTC()
	}
	if err := s.store.queries.UpsertSnapshotCursor(ctx, sqldb.UpsertSnapshotCursorParams{
		RequestID:          cursor.RequestID,
		RunnerID:           nullableString(cursor.RunnerID),
		CheckpointSequence: cursor.CheckpointSequence,
		EventSequence:      int64(cursor.EventSequence),
		UpdatedAt:          cursor.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return fmt.Errorf("sqlite: save snapshot cursor %q: %w", cursor.RequestID, err)
	}
	return nil
}

// LoadCursor returns the stored cursor for requestID.
func (s *SnapshotCursorStore) LoadCursor(ctx context.Context, requestID string) (storepkg.SnapshotCursor, error) {
	if s == nil || s.store == nil || s.store.queries == nil {
		return storepkg.SnapshotCursor{}, fmt.Errorf("sqlite: SnapshotCursorStore.LoadCursor called on nil store")
	}
	if requestID == "" {
		return storepkg.SnapshotCursor{}, fmt.Errorf("sqlite: snapshot cursor request_id must not be empty")
	}
	row, err := s.store.queries.GetSnapshotCursor(ctx, requestID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storepkg.SnapshotCursor{}, storepkg.ErrSnapshotCursorNotFound
		}
		return storepkg.SnapshotCursor{}, fmt.Errorf("sqlite: load snapshot cursor %q: %w", requestID, err)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, row.UpdatedAt)
	if err != nil {
		return storepkg.SnapshotCursor{}, fmt.Errorf("sqlite: decode snapshot cursor %q timestamp: %w", requestID, err)
	}
	if row.EventSequence < 0 {
		return storepkg.SnapshotCursor{}, fmt.Errorf("sqlite: decode snapshot cursor %q: negative event_sequence %d", requestID, row.EventSequence)
	}
	return storepkg.SnapshotCursor{
		RequestID:          row.RequestID,
		RunnerID:           row.RunnerID.String,
		CheckpointSequence: row.CheckpointSequence,
		EventSequence:      uint64(row.EventSequence),
		UpdatedAt:          updatedAt,
	}, nil
}
