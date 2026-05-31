package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	storepkg "github.com/tsumina/dango/store"
)

var _ storepkg.SnapshotCursorStore = (*SnapshotCursorStore)(nil)

// SnapshotCursorStore persists request-scoped describe replay cursors in
// Postgres.
type SnapshotCursorStore struct {
	store *Store
}

// NewSnapshotCursorStore returns a Postgres-backed store for snapshot cursors.
func NewSnapshotCursorStore(store *Store) *SnapshotCursorStore {
	return &SnapshotCursorStore{store: store}
}

// SaveCursor inserts or replaces the cursor for cursor.RequestID.
func (s *SnapshotCursorStore) SaveCursor(ctx context.Context, cursor storepkg.SnapshotCursor) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return fmt.Errorf("postgres: SnapshotCursorStore.SaveCursor called on nil store")
	}
	if cursor.RequestID == "" {
		return fmt.Errorf("postgres: snapshot cursor missing request_id")
	}
	if cursor.CheckpointSequence < 0 {
		return fmt.Errorf("postgres: snapshot cursor checkpoint_sequence must be non-negative")
	}
	if cursor.EventSequence > math.MaxInt64 {
		return fmt.Errorf("postgres: snapshot cursor event_sequence %d exceeds int64", cursor.EventSequence)
	}
	if cursor.UpdatedAt.IsZero() {
		cursor.UpdatedAt = time.Now().UTC()
	}
	if _, err := s.store.db.ExecContext(ctx, `
		INSERT INTO snapshot_cursors (
			request_id, runner_id, checkpoint_sequence, event_sequence, updated_at
		)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (request_id) DO UPDATE SET
			runner_id = EXCLUDED.runner_id,
			checkpoint_sequence = EXCLUDED.checkpoint_sequence,
			event_sequence = EXCLUDED.event_sequence,
			updated_at = EXCLUDED.updated_at
	`,
		cursor.RequestID,
		nullableString(cursor.RunnerID),
		cursor.CheckpointSequence,
		int64(cursor.EventSequence),
		cursor.UpdatedAt.UTC(),
	); err != nil {
		return fmt.Errorf("postgres: save snapshot cursor %q: %w", cursor.RequestID, err)
	}
	return nil
}

// LoadCursor returns the stored cursor for requestID.
func (s *SnapshotCursorStore) LoadCursor(ctx context.Context, requestID string) (storepkg.SnapshotCursor, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return storepkg.SnapshotCursor{}, fmt.Errorf("postgres: SnapshotCursorStore.LoadCursor called on nil store")
	}
	if requestID == "" {
		return storepkg.SnapshotCursor{}, fmt.Errorf("postgres: snapshot cursor request_id must not be empty")
	}
	var (
		runnerID           sql.NullString
		checkpointSequence int64
		eventSequence      int64
		updatedAt          time.Time
	)
	err := s.store.db.QueryRowContext(ctx, `
		SELECT runner_id, checkpoint_sequence, event_sequence, updated_at
		FROM snapshot_cursors
		WHERE request_id = $1
	`, requestID).Scan(&runnerID, &checkpointSequence, &eventSequence, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storepkg.SnapshotCursor{}, storepkg.ErrSnapshotCursorNotFound
		}
		return storepkg.SnapshotCursor{}, fmt.Errorf("postgres: load snapshot cursor %q: %w", requestID, err)
	}
	if eventSequence < 0 {
		return storepkg.SnapshotCursor{}, fmt.Errorf("postgres: decode snapshot cursor %q: negative event_sequence %d", requestID, eventSequence)
	}
	return storepkg.SnapshotCursor{
		RequestID:          requestID,
		RunnerID:           runnerID.String,
		CheckpointSequence: checkpointSequence,
		EventSequence:      uint64(eventSequence),
		UpdatedAt:          updatedAt.UTC(),
	}, nil
}
