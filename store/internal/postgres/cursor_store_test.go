package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	storepkg "github.com/tsumina/dango/store"
)

func TestSnapshotCursorStore_SaveAndLoad(t *testing.T) {
	store, cleanup := mustPostgresStore(t)
	defer cleanup()

	cursorStore := NewSnapshotCursorStore(store)
	requestID := fmt.Sprintf("req_postgres_cursor_%d", time.Now().UnixNano())

	if _, err := cursorStore.LoadCursor(context.Background(), requestID); !errors.Is(err, storepkg.ErrSnapshotCursorNotFound) {
		t.Fatalf("LoadCursor(missing) err = %v, want ErrSnapshotCursorNotFound", err)
	}
	initial := storepkg.SnapshotCursor{
		RequestID:          requestID,
		RunnerID:           "run_postgres_cursor",
		CheckpointSequence: 1,
		EventSequence:      2,
	}
	if err := cursorStore.SaveCursor(context.Background(), initial); err != nil {
		t.Fatalf("SaveCursor(initial): %v", err)
	}
	updated := initial
	updated.CheckpointSequence = 3
	updated.EventSequence = 4
	if err := cursorStore.SaveCursor(context.Background(), updated); err != nil {
		t.Fatalf("SaveCursor(updated): %v", err)
	}
	loaded, err := cursorStore.LoadCursor(context.Background(), requestID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if loaded.CheckpointSequence != updated.CheckpointSequence || loaded.EventSequence != updated.EventSequence {
		t.Fatalf("loaded cursor = %+v, want checkpoint=%d event=%d", loaded, updated.CheckpointSequence, updated.EventSequence)
	}
	if loaded.UpdatedAt.IsZero() {
		t.Fatal("loaded UpdatedAt is zero")
	}
}
