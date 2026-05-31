package store

import (
	"context"
	"fmt"
	"testing"
)

func TestJSONSnapshotCursorStoreUsesFixedStripedLocks(t *testing.T) {
	store, err := NewJSONSnapshotCursorStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONSnapshotCursorStore: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < defaultStripedStoreLockCount*2; i++ {
		requestID := fmt.Sprintf("req_%03d", i)
		if err := store.SaveCursor(ctx, SnapshotCursor{RequestID: requestID, RunnerID: "runner", EventSequence: uint64(i + 1)}); err != nil {
			t.Fatalf("SaveCursor(%s): %v", requestID, err)
		}
	}
	if len(store.locks.locks) != defaultStripedStoreLockCount {
		t.Fatalf("lock stripe count = %d, want %d", len(store.locks.locks), defaultStripedStoreLockCount)
	}
	loaded, err := store.LoadCursor(ctx, "req_000")
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if loaded.RequestID != "req_000" {
		t.Fatalf("loaded request id = %q, want req_000", loaded.RequestID)
	}
}
