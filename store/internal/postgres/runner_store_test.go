package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	runnerpkg "github.com/tsumina/dango/runner"
)

func TestRunnerStore_AppendLoadAndDelete(t *testing.T) {
	store, cleanup := mustPostgresStore(t)
	defer cleanup()

	runnerStore := NewRunnerStore(store)
	runnerID := fmt.Sprintf("run_postgres_runner_%d", time.Now().UnixNano())

	if _, err := runnerStore.Append(context.Background(), runnerID, &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordStatus, Status: runnerpkg.RunnerStatusRunning}); !errors.Is(err, runnerpkg.ErrRunnerLogNotInitialised) {
		t.Fatalf("Append(non-init) err = %v, want ErrRunnerLogNotInitialised", err)
	}
	seq, err := runnerStore.Append(context.Background(), runnerID, &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit})
	if err != nil {
		t.Fatalf("Append(init): %v", err)
	}
	if seq != 1 {
		t.Fatalf("Append(init) seq = %d, want 1", seq)
	}
	if _, err := runnerStore.Append(context.Background(), runnerID, &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}); !errors.Is(err, runnerpkg.ErrRunnerLogAlreadyInitialised) {
		t.Fatalf("Append(duplicate init) err = %v, want ErrRunnerLogAlreadyInitialised", err)
	}
	seq, err = runnerStore.Append(context.Background(), runnerID, &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordStatus, Status: runnerpkg.RunnerStatusIdle})
	if err != nil {
		t.Fatalf("Append(status): %v", err)
	}
	if seq != 2 {
		t.Fatalf("Append(status) seq = %d, want 2", seq)
	}
	loaded, err := runnerStore.Load(context.Background(), runnerID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("len(loaded) = %d, want 2", len(loaded))
	}
	if err := runnerStore.Delete(context.Background(), runnerID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := runnerStore.Load(context.Background(), runnerID); !errors.Is(err, runnerpkg.ErrRunnerLogNotFound) {
		t.Fatalf("Load(after delete) err = %v, want ErrRunnerLogNotFound", err)
	}
}
