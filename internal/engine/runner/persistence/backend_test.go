package persistence

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	storepkg "github.com/tsumina/dango/internal/store"
)

func TestNoneBackendDisablesStores(t *testing.T) {
	t.Parallel()

	backend := None()
	if backend.EventLogStore() != nil {
		t.Fatal("EventLogStore() != nil for none backend")
	}
	if backend.RunnerStore() != nil {
		t.Fatal("RunnerStore() != nil for none backend")
	}
	if backend.SnapshotCursorStore() != nil {
		t.Fatal("SnapshotCursorStore() != nil for none backend")
	}
	if backend.WorkspaceRoot() != "" {
		t.Fatalf("WorkspaceRoot() = %q, want empty", backend.WorkspaceRoot())
	}
	if err := backend.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestMarkdownBackendStoresAndWorkspaceRoot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "persist")
	backend, err := NewMarkdownBackend(root)
	if err != nil {
		t.Fatalf("NewMarkdownBackend: %v", err)
	}

	workspaceRoot := backend.WorkspaceRoot()
	if workspaceRoot == "" {
		t.Fatal("WorkspaceRoot() = empty")
	}
	if _, err := os.Stat(workspaceRoot); err != nil {
		t.Fatalf("Stat(workspace root): %v", err)
	}

	eventDelta, err := json.Marshal(map[string]any{"message": "ok"})
	if err != nil {
		t.Fatalf("marshal delta: %v", err)
	}
	event := streampkg.Event{
		EventType:      streampkg.EventStatusProgress,
		From:           streampkg.Source{Layer: "orchestrator", ID: "or_1"},
		SequenceNumber: 1,
		LogicalTime:    1,
		Status:         streampkg.StatusRunning,
		Delta:          eventDelta,
		Timestamp:      time.Now().UTC(),
		Scope:          streampkg.Scope{RequestID: "req_1"},
	}
	if err := backend.EventLogStore().AppendEvent(ctx, event); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	events, err := backend.EventLogStore().LoadEvents(ctx, streampkg.Scope{RequestID: "req_1"}, 1, streampkg.Filter{})
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(events) != 1 || events[0].EventType != streampkg.EventStatusProgress {
		t.Fatalf("events = %+v, want one progress event", events)
	}

	if _, err := backend.RunnerStore().Append(ctx, "run_1", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}); err != nil {
		t.Fatalf("Append(init): %v", err)
	}
	records, err := backend.RunnerStore().Load(ctx, "run_1")
	if err != nil {
		t.Fatalf("Load runner records: %v", err)
	}
	if len(records) != 1 || records[0].Kind != runnerpkg.RunnerRecordInit {
		t.Fatalf("records = %+v, want one init", records)
	}

	cursor := storepkg.SnapshotCursor{RequestID: "req_1", RunnerID: "run_1", EventSequence: 1}
	if err := backend.SnapshotCursorStore().SaveCursor(ctx, cursor); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}
	loadedCursor, err := backend.SnapshotCursorStore().LoadCursor(ctx, "req_1")
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if loadedCursor.RequestID != "req_1" || loadedCursor.RunnerID != "run_1" || loadedCursor.EventSequence != 1 {
		t.Fatalf("loaded cursor = %+v", loadedCursor)
	}
}
