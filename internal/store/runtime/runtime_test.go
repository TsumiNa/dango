package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	storepkg "github.com/tsumina/dango/internal/store"
)

func TestOpen_DefaultJSONFallbackCreatesUsableStoresAndCleansUp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	persistence, err := Open(DefaultConfig())
	if err != nil {
		t.Fatalf("Open(default): %v", err)
	}
	root := persistence.RootDir()
	if root == "" {
		t.Fatal("RootDir() = empty, want temp persistence root")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("Stat(root): %v", err)
	}

	event := runtimeTestEvent("req_json_runtime", 1)
	if err := persistence.EventLogStore().AppendEvent(ctx, event); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	loadedEvents, err := persistence.EventLogStore().LoadEvents(ctx, streampkg.Scope{RequestID: event.Scope.RequestID}, 1, streampkg.Filter{EventTypes: []string{event.EventType}})
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(loadedEvents) != 1 || loadedEvents[0].EventType != event.EventType {
		t.Fatalf("loaded events = %+v, want one %s event", loadedEvents, event.EventType)
	}

	runnerStore := persistence.RunnerStore()
	if _, err := runnerStore.Append(ctx, "run_json_runtime", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}); err != nil {
		t.Fatalf("Append(init): %v", err)
	}
	if _, err := runnerStore.Append(ctx, "run_json_runtime", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordStatus, Status: runnerpkg.RunnerStatusIdle}); err != nil {
		t.Fatalf("Append(status): %v", err)
	}
	records, err := runnerStore.Load(ctx, "run_json_runtime")
	if err != nil {
		t.Fatalf("Load runner records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}

	cursor := storepkg.SnapshotCursor{RequestID: event.Scope.RequestID, RunnerID: "run_json_runtime", EventSequence: 1}
	if err := persistence.SnapshotCursorStore().SaveCursor(ctx, cursor); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}
	loadedCursor, err := persistence.SnapshotCursorStore().LoadCursor(ctx, cursor.RequestID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if loadedCursor.RequestID != cursor.RequestID || loadedCursor.RunnerID != cursor.RunnerID || loadedCursor.EventSequence != cursor.EventSequence {
		t.Fatalf("loaded cursor = %+v, want %+v", loadedCursor, cursor)
	}

	if err := persistence.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("Stat(root) after Close err = %v, want not exist", err)
	}
}

func TestOpen_SQLiteStoresSurviveReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "dango.db")
	persistence, err := Open(Config{SQLitePath: dbPath})
	if err != nil {
		t.Fatalf("Open(sqlite): %v", err)
	}
	workspaceRoot := persistence.Backend().WorkspaceRoot()
	wantWorkspaceRoot := filepath.Join(filepath.Dir(dbPath), "workspace")
	if workspaceRoot != wantWorkspaceRoot {
		t.Fatalf("WorkspaceRoot() = %q, want %q", workspaceRoot, wantWorkspaceRoot)
	}
	for _, mirrorDir := range []string{"event-log", "runner-log", "snapshot-cursor"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(dbPath), mirrorDir)); !os.IsNotExist(err) {
			t.Fatalf("%s unexpectedly exists in sqlite-only mode: %v", mirrorDir, err)
		}
	}
	event := runtimeTestEvent("req_sqlite_runtime", 1)
	if err := persistence.EventLogStore().AppendEvent(ctx, event); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if _, err := persistence.RunnerStore().Append(ctx, "run_sqlite_runtime", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}); err != nil {
		t.Fatalf("Append(init): %v", err)
	}
	if _, err := persistence.RunnerStore().Append(ctx, "run_sqlite_runtime", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordStatus, Status: runnerpkg.RunnerStatusIdle}); err != nil {
		t.Fatalf("Append(status): %v", err)
	}
	cursor := storepkg.SnapshotCursor{RequestID: event.Scope.RequestID, RunnerID: "run_sqlite_runtime", EventSequence: 1}
	if err := persistence.SnapshotCursorStore().SaveCursor(ctx, cursor); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}
	if err := persistence.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}

	reopened, err := Open(Config{SQLitePath: dbPath})
	if err != nil {
		t.Fatalf("Open(reopen): %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Fatalf("Close(reopen): %v", err)
		}
	}()
	loadedEvents, err := reopened.EventLogStore().LoadEvents(ctx, streampkg.Scope{RequestID: event.Scope.RequestID}, 1, streampkg.Filter{})
	if err != nil {
		t.Fatalf("LoadEvents(reopen): %v", err)
	}
	if len(loadedEvents) != 1 || loadedEvents[0].Scope.RequestID != event.Scope.RequestID {
		t.Fatalf("loaded events = %+v, want persisted request %q", loadedEvents, event.Scope.RequestID)
	}
	records, err := reopened.RunnerStore().Load(ctx, "run_sqlite_runtime")
	if err != nil {
		t.Fatalf("Load runner records(reopen): %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
	loadedCursor, err := reopened.SnapshotCursorStore().LoadCursor(ctx, cursor.RequestID)
	if err != nil {
		t.Fatalf("LoadCursor(reopen): %v", err)
	}
	if loadedCursor.EventSequence != cursor.EventSequence {
		t.Fatalf("loaded cursor event sequence = %d, want %d", loadedCursor.EventSequence, cursor.EventSequence)
	}
}

func TestOpen_SQLiteWithMarkdownMirrorWritesMirrorFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "dango.db")
	persistence, err := Open(Config{SQLitePath: dbPath, SQLiteMarkdownMirror: true})
	if err != nil {
		t.Fatalf("Open(sqlite+mirror): %v", err)
	}
	event := runtimeTestEvent("req_sqlite_mirror", 1)
	if err := persistence.EventLogStore().AppendEvent(ctx, event); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if _, err := persistence.RunnerStore().Append(ctx, "run_sqlite_mirror", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}); err != nil {
		t.Fatalf("Append(init): %v", err)
	}
	cursor := storepkg.SnapshotCursor{RequestID: event.Scope.RequestID, RunnerID: "run_sqlite_mirror", EventSequence: 1}
	if err := persistence.SnapshotCursorStore().SaveCursor(ctx, cursor); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}
	if err := persistence.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}

	mirrorRoot := filepath.Dir(dbPath)
	for _, file := range []string{
		filepath.Join(mirrorRoot, "event-log", event.Scope.RequestID+".jsonl"),
		filepath.Join(mirrorRoot, "runner-log", "run_sqlite_mirror.jsonl"),
		filepath.Join(mirrorRoot, "snapshot-cursor", event.Scope.RequestID+".json"),
	} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("Stat(%q): %v", file, err)
		}
	}

	reopened, err := Open(Config{SQLitePath: dbPath})
	if err != nil {
		t.Fatalf("Open(reopen sqlite-only): %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Fatalf("Close(reopen): %v", err)
		}
	}()
	loadedEvents, err := reopened.EventLogStore().LoadEvents(ctx, streampkg.Scope{RequestID: event.Scope.RequestID}, 1, streampkg.Filter{})
	if err != nil {
		t.Fatalf("LoadEvents(reopen): %v", err)
	}
	if len(loadedEvents) != 1 || loadedEvents[0].Scope.RequestID != event.Scope.RequestID {
		t.Fatalf("loaded events = %+v, want persisted request %q", loadedEvents, event.Scope.RequestID)
	}
	records, err := reopened.RunnerStore().Load(ctx, "run_sqlite_mirror")
	if err != nil {
		t.Fatalf("Load runner records(reopen): %v", err)
	}
	if len(records) != 1 || records[0].Kind != runnerpkg.RunnerRecordInit {
		t.Fatalf("records = %+v, want one init", records)
	}
	loadedCursor, err := reopened.SnapshotCursorStore().LoadCursor(ctx, cursor.RequestID)
	if err != nil {
		t.Fatalf("LoadCursor(reopen): %v", err)
	}
	if loadedCursor.EventSequence != cursor.EventSequence {
		t.Fatalf("loaded cursor event sequence = %d, want %d", loadedCursor.EventSequence, cursor.EventSequence)
	}
}

func TestOpen_RejectsUnusableSQLitePath(t *testing.T) {
	t.Parallel()

	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile(blocked): %v", err)
	}
	_, err := Open(Config{SQLitePath: filepath.Join(blocked, "dango.db")})
	if err == nil {
		t.Fatal("Open accepted unusable sqlite path")
	}
}

func TestOpen_SQLiteBackendCloseReleasesUnderlyingResources(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "dango.db")
	persistence, err := Open(Config{SQLitePath: dbPath})
	if err != nil {
		t.Fatalf("Open(sqlite): %v", err)
	}
	event := runtimeTestEvent("req_sqlite_backend_close", 1)
	if err := persistence.Backend().Close(ctx); err != nil {
		t.Fatalf("Backend().Close: %v", err)
	}
	if err := persistence.EventLogStore().AppendEvent(ctx, event); err == nil {
		t.Fatal("AppendEvent succeeded after Backend().Close, want closed backing store")
	}
	if err := persistence.Close(); err != nil {
		t.Fatalf("Close(after backend close): %v", err)
	}
}

func TestCompositeEventLogStore_AppendEventIgnoresMirrorFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	event := runtimeTestEvent("req_composite_event", 1)
	primary := &runtimeTestEventLogStore{}
	mirror := &runtimeTestEventLogStore{appendErr: errors.New("mirror append failed")}
	store := &compositeEventLogStore{
		primary: primary,
		mirror:  mirror,
	}

	if err := store.AppendEvent(ctx, event); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if primary.appendCalls != 1 {
		t.Fatalf("primary append calls = %d, want 1", primary.appendCalls)
	}
	if mirror.appendCalls != 1 {
		t.Fatalf("mirror append calls = %d, want 1", mirror.appendCalls)
	}
}

func TestCompositeRunnerStore_AppendKeepsCallerSequence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rec := &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}
	primary := &runtimeTestRunnerStore{
		appendFn: func(_ context.Context, _ string, in *runnerpkg.RunnerRecord) (int64, error) {
			in.Seq = 11
			return 11, nil
		},
	}
	var mirrorRec *runnerpkg.RunnerRecord
	mirror := &runtimeTestRunnerStore{
		appendFn: func(_ context.Context, _ string, in *runnerpkg.RunnerRecord) (int64, error) {
			mirrorRec = in
			if in.Seq != 11 {
				t.Fatalf("mirror input seq = %d, want 11", in.Seq)
			}
			in.Seq = 100
			return in.Seq, nil
		},
	}
	store := &compositeRunnerStore{
		primary: primary,
		mirror:  mirror,
	}

	seq, err := store.Append(ctx, "run_composite_seq", rec)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if seq != 11 {
		t.Fatalf("Append seq = %d, want 11", seq)
	}
	if rec.Seq != 11 {
		t.Fatalf("caller record seq = %d, want 11", rec.Seq)
	}
	if mirrorRec == nil {
		t.Fatal("mirror record pointer was nil")
	}
	if mirrorRec == rec {
		t.Fatal("mirror append received caller record pointer; want clone")
	}
}

type runtimeTestEventLogStore struct {
	appendErr   error
	appendCalls int
}

func (s *runtimeTestEventLogStore) AppendEvent(context.Context, streampkg.Event) error {
	s.appendCalls++
	return s.appendErr
}

func (s *runtimeTestEventLogStore) LoadEvents(context.Context, streampkg.Scope, uint64, streampkg.Filter) ([]streampkg.Event, error) {
	return nil, nil
}

type runtimeTestRunnerStore struct {
	appendFn func(context.Context, string, *runnerpkg.RunnerRecord) (int64, error)
}

func (s *runtimeTestRunnerStore) Append(ctx context.Context, runnerID string, rec *runnerpkg.RunnerRecord) (int64, error) {
	if s.appendFn == nil {
		return 0, nil
	}
	return s.appendFn(ctx, runnerID, rec)
}

func (s *runtimeTestRunnerStore) Load(context.Context, string) ([]runnerpkg.RunnerRecord, error) {
	return nil, nil
}

func (s *runtimeTestRunnerStore) Delete(context.Context, string) error {
	return nil
}

func runtimeTestEvent(requestID string, sequence uint64) streampkg.Event {
	delta, err := json.Marshal(map[string]any{"message": "runtime persistence"})
	if err != nil {
		panic(err)
	}
	return streampkg.Event{
		EventType:      streampkg.EventStatusProgress,
		From:           streampkg.Source{Layer: "orchestrator", ID: "orchestrator"},
		SequenceNumber: sequence,
		LogicalTime:    sequence,
		Status:         streampkg.StatusRunning,
		Delta:          delta,
		Timestamp:      time.Unix(1_700_000_000+int64(sequence), 0).UTC(),
		Scope:          streampkg.Scope{RequestID: requestID},
	}
}
