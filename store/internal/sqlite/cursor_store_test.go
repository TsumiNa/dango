package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	storepkg "github.com/tsumina/dango/store"
)

func TestSnapshotCursorStoreRoundTrip(t *testing.T) {
	t.Parallel()

	dbStore, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := dbStore.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	cursorStore := NewSnapshotCursorStore(dbStore)
	want := storepkg.SnapshotCursor{
		RequestID:          "req_1",
		RunnerID:           "run_1",
		CheckpointSequence: 4,
		EventSequence:      9,
	}
	if err := cursorStore.SaveCursor(context.Background(), want); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}

	got, err := cursorStore.LoadCursor(context.Background(), want.RequestID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if got.RequestID != want.RequestID {
		t.Fatalf("RequestID = %q, want %q", got.RequestID, want.RequestID)
	}
	if got.RunnerID != want.RunnerID {
		t.Fatalf("RunnerID = %q, want %q", got.RunnerID, want.RunnerID)
	}
	if got.CheckpointSequence != want.CheckpointSequence {
		t.Fatalf("CheckpointSequence = %d, want %d", got.CheckpointSequence, want.CheckpointSequence)
	}
	if got.EventSequence != want.EventSequence {
		t.Fatalf("EventSequence = %d, want %d", got.EventSequence, want.EventSequence)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt = zero, want cursor timestamp")
	}

	want.RunnerID = "run_2"
	want.CheckpointSequence = 7
	want.EventSequence = 12
	if err := cursorStore.SaveCursor(context.Background(), want); err != nil {
		t.Fatalf("SaveCursor(update): %v", err)
	}

	updated, err := cursorStore.LoadCursor(context.Background(), want.RequestID)
	if err != nil {
		t.Fatalf("LoadCursor(update): %v", err)
	}
	if updated.RunnerID != want.RunnerID {
		t.Fatalf("updated RunnerID = %q, want %q", updated.RunnerID, want.RunnerID)
	}
	if updated.CheckpointSequence != want.CheckpointSequence {
		t.Fatalf("updated CheckpointSequence = %d, want %d", updated.CheckpointSequence, want.CheckpointSequence)
	}
	if updated.EventSequence != want.EventSequence {
		t.Fatalf("updated EventSequence = %d, want %d", updated.EventSequence, want.EventSequence)
	}
}

func TestSnapshotCursorStoreLoadMissing(t *testing.T) {
	t.Parallel()

	dbStore, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := dbStore.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	_, err = NewSnapshotCursorStore(dbStore).LoadCursor(context.Background(), "missing")
	if !errors.Is(err, storepkg.ErrSnapshotCursorNotFound) {
		t.Fatalf("LoadCursor err = %v, want ErrSnapshotCursorNotFound", err)
	}
}
