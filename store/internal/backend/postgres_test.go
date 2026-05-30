package backend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	runnerpkg "github.com/tsumina/dango/engine/runner"
	storepkg "github.com/tsumina/dango/store"
	streampkg "github.com/tsumina/dango/stream"
)

const postgresTestDSNEnv = "DANGO_POSTGRES_TEST_DSN"

func TestPostgresBackendStoresAndWorkspaceRootAfterReopen(t *testing.T) {
	dsn := postgresTestDSN(t)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	requestID := "req_postgres_backend_" + suffix
	runnerID := "run_postgres_backend_" + suffix
	ctx := context.Background()
	workspaceRoot := filepath.Join(t.TempDir(), "workspace")

	backend, err := NewPostgresBackend(dsn, workspaceRoot)
	if err != nil {
		t.Fatalf("NewPostgresBackend: %v", err)
	}
	if backend.WorkspaceRoot() != workspaceRoot {
		t.Fatalf("WorkspaceRoot() = %q, want %q", backend.WorkspaceRoot(), workspaceRoot)
	}
	if _, err := os.Stat(workspaceRoot); err != nil {
		t.Fatalf("Stat(workspace root): %v", err)
	}

	event := streampkg.Event{
		EventType:      streampkg.EventStatusProgress,
		From:           streampkg.Source{Layer: "orchestrator", ID: "or_1"},
		SequenceNumber: 1,
		LogicalTime:    1,
		Status:         streampkg.StatusRunning,
		Delta:          json.RawMessage(`{"message":"ok"}`),
		Timestamp:      time.Now().UTC(),
		Scope:          streampkg.Scope{RequestID: requestID},
	}
	if err := backend.EventLogStore().AppendEvent(ctx, event); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if _, err := backend.RunnerStore().Append(ctx, runnerID, &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}); err != nil {
		t.Fatalf("Append(init): %v", err)
	}
	cursor := storepkg.SnapshotCursor{RequestID: requestID, RunnerID: runnerID, EventSequence: 1}
	if err := backend.SnapshotCursorStore().SaveCursor(ctx, cursor); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}
	if err := backend.Close(ctx); err != nil {
		t.Fatalf("Close(first backend): %v", err)
	}

	reopened, err := NewPostgresBackend(dsn, workspaceRoot)
	if err != nil {
		t.Fatalf("NewPostgresBackend(reopen): %v", err)
	}
	defer func() {
		if err := reopened.Close(ctx); err != nil {
			t.Fatalf("Close(reopened backend): %v", err)
		}
	}()
	events, err := reopened.EventLogStore().LoadEvents(ctx, streampkg.Scope{RequestID: requestID}, 1, streampkg.Filter{})
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 1 || events[0].EventType != streampkg.EventStatusProgress {
		t.Fatalf("events = %+v, want one progress event", events)
	}
	records, err := reopened.RunnerStore().Load(ctx, runnerID)
	if err != nil {
		t.Fatalf("Load runner records: %v", err)
	}
	if len(records) != 1 || records[0].Kind != runnerpkg.RunnerRecordInit {
		t.Fatalf("records = %+v, want one init", records)
	}
	loadedCursor, err := reopened.SnapshotCursorStore().LoadCursor(ctx, requestID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if loadedCursor.RequestID != requestID || loadedCursor.RunnerID != runnerID || loadedCursor.EventSequence != 1 {
		t.Fatalf("loaded cursor = %+v", loadedCursor)
	}
}

func TestPostgresBackendRejectsMissingConfig(t *testing.T) {
	t.Parallel()

	if _, err := NewPostgresBackend("", t.TempDir()); err == nil {
		t.Fatal("NewPostgresBackend accepted empty dsn")
	}
	if _, err := NewPostgresBackend("postgres://example", ""); err == nil {
		t.Fatal("NewPostgresBackend accepted empty workspace root")
	}
}

func postgresTestDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(postgresTestDSNEnv))
	if dsn == "" {
		t.Skipf("%s is not set; skipping postgres integration test", postgresTestDSNEnv)
	}
	return dsn
}
