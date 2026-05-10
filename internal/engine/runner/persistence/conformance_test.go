package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	persistencepkg "github.com/tsumina/dango/internal/engine/runner/persistence"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	storepkg "github.com/tsumina/dango/internal/store"
	runtimepkg "github.com/tsumina/dango/internal/store/runtime"
)

type backendCase struct {
	name   string
	open   func(t *testing.T) (persistencepkg.Backend, func())
	isNoop bool
}

const postgresTestDSNEnv = "DANGO_POSTGRES_TEST_DSN"

type conformanceFixture struct {
	requestID string
	runnerID  string
	fromNode  string
	toNode    string

	exchangePath     string
	handoffInboxPath string
	handoffPath      string
	artifactPath     string
	memoSnapshotDir  string

	events        []streampkg.Event
	cursorInitial storepkg.SnapshotCursor
	cursorUpdated storepkg.SnapshotCursor
}

func TestBackendConformance(t *testing.T) {
	t.Parallel()

	for _, tc := range backendCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			backend, cleanup := tc.open(t)
			t.Cleanup(cleanup)

			if tc.isNoop {
				assertNoopBackendContract(t, backend)
				return
			}

			fixture := newConformanceFixture(t)
			assertDurableBackendContract(t, backend)
			assertPathRuleResolution(t, backend, fixture)
			assertEventLogContract(t, backend, fixture)
			assertRunnerStoreContract(t, backend, fixture)
			assertCursorContract(t, backend, fixture)
		})
	}
}

func backendCases() []backendCase {
	cases := []backendCase{
		{
			name: "none-noop",
			open: func(t *testing.T) (persistencepkg.Backend, func()) {
				t.Helper()
				return persistencepkg.None(), func() {}
			},
			isNoop: true,
		},
		{
			name: "markdown",
			open: func(t *testing.T) (persistencepkg.Backend, func()) {
				t.Helper()
				backend, err := persistencepkg.NewMarkdownBackend(filepath.Join(t.TempDir(), "markdown"))
				if err != nil {
					t.Fatalf("NewMarkdownBackend: %v", err)
				}
				return backend, func() {
					if err := backend.Close(context.Background()); err != nil {
						t.Fatalf("markdown backend Close: %v", err)
					}
				}
			},
		},
		{
			name: "sqlite",
			open: func(t *testing.T) (persistencepkg.Backend, func()) {
				t.Helper()
				backend, err := persistencepkg.NewSQLiteBackend(filepath.Join(t.TempDir(), "dango.db"))
				if err != nil {
					t.Fatalf("NewSQLiteBackend: %v", err)
				}
				return backend, func() {
					if err := backend.Close(context.Background()); err != nil {
						t.Fatalf("sqlite backend Close: %v", err)
					}
				}
			},
		},
		{
			name: "postgres",
			open: func(t *testing.T) (persistencepkg.Backend, func()) {
				t.Helper()
				dsn := strings.TrimSpace(os.Getenv(postgresTestDSNEnv))
				if dsn == "" {
					t.Skipf("%s is not set; skipping postgres conformance case", postgresTestDSNEnv)
				}
				backend, err := persistencepkg.NewPostgresBackend(dsn, filepath.Join(t.TempDir(), "workspace"))
				if err != nil {
					t.Fatalf("NewPostgresBackend: %v", err)
				}
				return backend, func() {
					if err := backend.Close(context.Background()); err != nil {
						t.Fatalf("postgres backend Close: %v", err)
					}
				}
			},
		},
		{
			name: "composite-runtime",
			open: func(t *testing.T) (persistencepkg.Backend, func()) {
				t.Helper()
				dbPath := filepath.Join(t.TempDir(), "dango.db")
				persistence, err := runtimepkg.Open(runtimepkg.Config{SQLitePath: dbPath})
				if err != nil {
					t.Fatalf("runtime.Open: %v", err)
				}
				return persistence.Backend(), func() {
					if err := persistence.Close(); err != nil {
						t.Fatalf("runtime persistence Close: %v", err)
					}
				}
			},
		},
	}

	return cases
}

func newConformanceFixture(t *testing.T) conformanceFixture {
	t.Helper()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	requestID := "req_conformance_" + suffix
	runnerID := "run_conformance_" + suffix
	const (
		fromNode = "node_producer"
		toNode   = "node_consumer"
	)
	baseTime := time.Unix(1_700_000_000, 0).UTC()

	exchangePath := "exchange/node_producer.md"
	handoffInboxPath := filepath.ToSlash(filepath.Join("skills", toNode, "upstream", fromNode))
	handoffPath := filepath.ToSlash(filepath.Join(handoffInboxPath, "handoff.md"))
	artifactPath := filepath.ToSlash(filepath.Join(handoffInboxPath, "artifacts", "predictions.csv"))
	memoSnapshotDir := filepath.ToSlash(filepath.Join("skills", fromNode, "memo", "snapshots", "20260510T005530Z"))

	events := []streampkg.Event{
		{
			EventType:      streampkg.EventExchangePublished,
			From:           streampkg.Source{Layer: "runner", ID: runnerID},
			Status:         streampkg.StatusCompleted,
			SequenceNumber: 1,
			LogicalTime:    1,
			Timestamp:      baseTime,
			Scope: streampkg.Scope{
				RequestID: requestID,
				RunnerID:  runnerID,
				NodeID:    fromNode,
			},
			Delta: mustJSON(t, streampkg.ExchangePublishedPayload{
				ChannelHeader: streampkg.ChannelHeader{
					RunnerID:  runnerID,
					CreatedAt: baseTime,
				},
				NodeID:   fromNode,
				Path:     exchangePath,
				Document: "exchange result",
				Title:    "Producer result",
			}),
		},
		{
			EventType:      streampkg.EventHandoffDelivered,
			From:           streampkg.Source{Layer: "runner", ID: runnerID},
			Status:         streampkg.StatusCompleted,
			SequenceNumber: 2,
			LogicalTime:    2,
			Timestamp:      baseTime.Add(time.Second),
			Scope: streampkg.Scope{
				RequestID: requestID,
				RunnerID:  runnerID,
				NodeID:    toNode,
			},
			Delta: mustJSON(t, streampkg.HandoffDeliveredPayload{
				RunnerID:      runnerID,
				FromNode:      fromNode,
				ToNode:        toNode,
				InboxPath:     handoffInboxPath,
				HandoffPath:   handoffPath,
				ArtifactPaths: []string{artifactPath},
				Artifacts: []streampkg.HandoffArtifactPayload{{
					Path:        artifactPath,
					Type:        runnerpkg.HandoffArtifactFile,
					Description: "Model predictions",
				}},
				DeliveredAt: baseTime.Add(time.Second),
			}),
		},
		{
			EventType:      streampkg.EventArtifactCreated,
			From:           streampkg.Source{Layer: "executor", ID: "ex_node_producer"},
			Status:         streampkg.StatusCompleted,
			SequenceNumber: 3,
			LogicalTime:    3,
			Timestamp:      baseTime.Add(2 * time.Second),
			Scope: streampkg.Scope{
				RequestID: requestID,
				RunnerID:  runnerID,
				NodeID:    fromNode,
			},
			Delta: mustJSON(t, map[string]any{
				"path":          artifactPath,
				"declared_path": "downstream/artifacts/predictions.csv",
				"resource_type": runnerpkg.HandoffArtifactFile,
				"description":   "Model predictions",
				"intent":        "handoff downstream scoring input",
			}),
		},
		{
			EventType:      streampkg.EventMemoSnapshot,
			From:           streampkg.Source{Layer: "executor", ID: "ex_node_producer"},
			Status:         streampkg.StatusCompleted,
			SequenceNumber: 4,
			LogicalTime:    4,
			Timestamp:      baseTime.Add(3 * time.Second),
			Scope: streampkg.Scope{
				RequestID: requestID,
				RunnerID:  runnerID,
				NodeID:    fromNode,
			},
			Delta: mustJSON(t, streampkg.MemoSnapshotPayload{
				RunnerID:    runnerID,
				NodeID:      fromNode,
				SkillName:   "producer-skill",
				SnapshotDir: memoSnapshotDir,
				SnapshotAt:  baseTime.Add(3 * time.Second),
			}),
		},
	}

	return conformanceFixture{
		requestID: requestID,
		runnerID:  runnerID,
		fromNode:  fromNode,
		toNode:    toNode,

		exchangePath:     exchangePath,
		handoffInboxPath: handoffInboxPath,
		handoffPath:      handoffPath,
		artifactPath:     artifactPath,
		memoSnapshotDir:  memoSnapshotDir,

		events: events,
		cursorInitial: storepkg.SnapshotCursor{
			RequestID:          requestID,
			RunnerID:           runnerID,
			CheckpointSequence: 1,
			EventSequence:      2,
		},
		cursorUpdated: storepkg.SnapshotCursor{
			RequestID:          requestID,
			RunnerID:           runnerID,
			CheckpointSequence: 2,
			EventSequence:      4,
		},
	}
}

func assertNoopBackendContract(t *testing.T, backend persistencepkg.Backend) {
	t.Helper()

	if backend.EventLogStore() != nil {
		t.Fatal("none backend EventLogStore() != nil")
	}
	if backend.RunnerStore() != nil {
		t.Fatal("none backend RunnerStore() != nil")
	}
	if backend.SnapshotCursorStore() != nil {
		t.Fatal("none backend SnapshotCursorStore() != nil")
	}
	if backend.WorkspaceRoot() != "" {
		t.Fatalf("none backend WorkspaceRoot() = %q, want empty", backend.WorkspaceRoot())
	}
	if err := backend.Close(context.Background()); err != nil {
		t.Fatalf("none backend Close: %v", err)
	}
}

func assertDurableBackendContract(t *testing.T, backend persistencepkg.Backend) {
	t.Helper()

	if backend.EventLogStore() == nil {
		t.Fatal("EventLogStore() = nil for durable backend")
	}
	if backend.RunnerStore() == nil {
		t.Fatal("RunnerStore() = nil for durable backend")
	}
	if backend.SnapshotCursorStore() == nil {
		t.Fatal("SnapshotCursorStore() = nil for durable backend")
	}
	workspaceRoot := backend.WorkspaceRoot()
	if workspaceRoot == "" {
		t.Fatal("WorkspaceRoot() = empty for durable backend")
	}
	info, err := os.Stat(workspaceRoot)
	if err != nil {
		t.Fatalf("Stat(workspace root): %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("workspace root %q is not a directory", workspaceRoot)
	}
}

func assertPathRuleResolution(t *testing.T, backend persistencepkg.Backend, fixture conformanceFixture) {
	t.Helper()

	canonicalWorkspaceRoot, err := filepath.EvalSymlinks(backend.WorkspaceRoot())
	if err != nil {
		t.Fatalf("EvalSymlinks(workspace root): %v", err)
	}
	workspace, err := runnerpkg.ProvisionWorkspace(
		backend.WorkspaceRoot(),
		fixture.runnerID,
		[]string{fixture.fromNode, fixture.toNode},
		persistencepkg.DefaultPathRule,
	)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	wantRunnerRoot := filepath.Join(canonicalWorkspaceRoot, persistencepkg.DefaultPathRule(fixture.runnerID))
	if workspace.Root() != wantRunnerRoot {
		t.Fatalf("workspace root = %q, want %q", workspace.Root(), wantRunnerRoot)
	}
}

func assertEventLogContract(t *testing.T, backend persistencepkg.Backend, fixture conformanceFixture) {
	t.Helper()
	ctx := context.Background()

	for _, event := range fixture.events {
		if err := backend.EventLogStore().AppendEvent(ctx, event); err != nil {
			t.Fatalf("AppendEvent(%s/%d): %v", event.EventType, event.SequenceNumber, err)
		}
	}

	loaded, err := backend.EventLogStore().LoadEvents(ctx, streampkg.Scope{RequestID: fixture.requestID}, 1, streampkg.Filter{})
	if err != nil {
		t.Fatalf("LoadEvents(all): %v", err)
	}
	if len(loaded) != len(fixture.events) {
		t.Fatalf("len(loaded) = %d, want %d", len(loaded), len(fixture.events))
	}
	for i := range loaded {
		if loaded[i].EventType != fixture.events[i].EventType || loaded[i].SequenceNumber != fixture.events[i].SequenceNumber {
			t.Fatalf("loaded[%d] = (%s,%d), want (%s,%d)", i, loaded[i].EventType, loaded[i].SequenceNumber, fixture.events[i].EventType, fixture.events[i].SequenceNumber)
		}
	}

	var exchange streampkg.ExchangePublishedPayload
	if err := json.Unmarshal(loaded[0].Delta, &exchange); err != nil {
		t.Fatalf("unmarshal exchange payload: %v", err)
	}
	if exchange.Path != fixture.exchangePath || exchange.NodeID != fixture.fromNode {
		t.Fatalf("exchange payload = %+v", exchange)
	}

	var handoff streampkg.HandoffDeliveredPayload
	if err := json.Unmarshal(loaded[1].Delta, &handoff); err != nil {
		t.Fatalf("unmarshal handoff delivery payload: %v", err)
	}
	if handoff.InboxPath != fixture.handoffInboxPath || handoff.HandoffPath != fixture.handoffPath || handoff.ToNode != fixture.toNode {
		t.Fatalf("handoff payload = %+v", handoff)
	}
	if len(handoff.Artifacts) != 1 || handoff.Artifacts[0].Path != fixture.artifactPath {
		t.Fatalf("handoff artifacts = %+v", handoff.Artifacts)
	}

	var artifactDelta map[string]any
	if err := json.Unmarshal(loaded[2].Delta, &artifactDelta); err != nil {
		t.Fatalf("unmarshal artifact payload: %v", err)
	}
	if artifactDelta["path"] != fixture.artifactPath || artifactDelta["declared_path"] != "downstream/artifacts/predictions.csv" || artifactDelta["resource_type"] != runnerpkg.HandoffArtifactFile {
		t.Fatalf("artifact delta = %+v", artifactDelta)
	}

	var memo streampkg.MemoSnapshotPayload
	if err := json.Unmarshal(loaded[3].Delta, &memo); err != nil {
		t.Fatalf("unmarshal memo snapshot payload: %v", err)
	}
	if memo.SnapshotDir != fixture.memoSnapshotDir || memo.NodeID != fixture.fromNode {
		t.Fatalf("memo snapshot payload = %+v", memo)
	}

	artifactOnly, err := backend.EventLogStore().LoadEvents(ctx, streampkg.Scope{RequestID: fixture.requestID}, 1, streampkg.Filter{
		EventTypes: []string{streampkg.EventArtifactCreated},
		Scope:      streampkg.Scope{RunnerID: fixture.runnerID, NodeID: fixture.fromNode},
	})
	if err != nil {
		t.Fatalf("LoadEvents(artifact filter): %v", err)
	}
	if len(artifactOnly) != 1 || artifactOnly[0].EventType != streampkg.EventArtifactCreated {
		t.Fatalf("artifact-only replay = %+v", artifactOnly)
	}
}

func assertRunnerStoreContract(t *testing.T, backend persistencepkg.Backend, fixture conformanceFixture) {
	t.Helper()
	ctx := context.Background()
	store := backend.RunnerStore()

	coldRunnerID := fixture.runnerID + "_cold"
	if _, err := store.Append(ctx, coldRunnerID, &runnerpkg.RunnerRecord{
		Kind:   runnerpkg.RunnerRecordStatus,
		Status: runnerpkg.RunnerStatusRunning,
	}); !errors.Is(err, runnerpkg.ErrRunnerLogNotInitialised) {
		t.Fatalf("Append(non-init first) err = %v, want ErrRunnerLogNotInitialised", err)
	}

	seq, err := store.Append(ctx, fixture.runnerID, &runnerpkg.RunnerRecord{
		Kind: runnerpkg.RunnerRecordInit,
	})
	if err != nil {
		t.Fatalf("Append(init): %v", err)
	}
	if seq != 1 {
		t.Fatalf("Append(init) seq = %d, want 1", seq)
	}

	if _, err := store.Append(ctx, fixture.runnerID, &runnerpkg.RunnerRecord{
		Kind: runnerpkg.RunnerRecordInit,
	}); !errors.Is(err, runnerpkg.ErrRunnerLogAlreadyInitialised) {
		t.Fatalf("Append(duplicate init) err = %v, want ErrRunnerLogAlreadyInitialised", err)
	}

	seq, err = store.Append(ctx, fixture.runnerID, &runnerpkg.RunnerRecord{
		Kind:   runnerpkg.RunnerRecordStatus,
		Status: runnerpkg.RunnerStatusRunning,
	})
	if err != nil {
		t.Fatalf("Append(status): %v", err)
	}
	if seq != 2 {
		t.Fatalf("Append(status) seq = %d, want 2", seq)
	}

	seq, err = store.Append(ctx, fixture.runnerID, &runnerpkg.RunnerRecord{
		Kind: runnerpkg.RunnerRecordEvent,
		Event: &runnerpkg.StoredRunnerEvent{
			Type:         runnerpkg.EventNodeCompleted.String(),
			NodeID:       fixture.toNode,
			DataEncoding: "json",
			DataJSON:     mustJSON(t, map[string]any{"ok": true}),
		},
	})
	if err != nil {
		t.Fatalf("Append(event): %v", err)
	}
	if seq != 3 {
		t.Fatalf("Append(event) seq = %d, want 3", seq)
	}

	records, err := store.Load(ctx, fixture.runnerID)
	if err != nil {
		t.Fatalf("Load(records): %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("len(records) = %d, want 3", len(records))
	}
	for i, record := range records {
		if record.Seq != int64(i+1) {
			t.Fatalf("records[%d].Seq = %d, want %d", i, record.Seq, i+1)
		}
	}
	if records[2].Event == nil || records[2].Event.Type != runnerpkg.EventNodeCompleted.String() || records[2].Event.NodeID != fixture.toNode {
		t.Fatalf("event record = %+v", records[2].Event)
	}
}

func assertCursorContract(t *testing.T, backend persistencepkg.Backend, fixture conformanceFixture) {
	t.Helper()
	ctx := context.Background()
	store := backend.SnapshotCursorStore()

	if err := store.SaveCursor(ctx, fixture.cursorInitial); err != nil {
		t.Fatalf("SaveCursor(initial): %v", err)
	}
	if err := store.SaveCursor(ctx, fixture.cursorUpdated); err != nil {
		t.Fatalf("SaveCursor(updated): %v", err)
	}
	cursor, err := store.LoadCursor(ctx, fixture.requestID)
	if err != nil {
		t.Fatalf("LoadCursor: %v", err)
	}
	if cursor.RequestID != fixture.cursorUpdated.RequestID ||
		cursor.RunnerID != fixture.cursorUpdated.RunnerID ||
		cursor.CheckpointSequence != fixture.cursorUpdated.CheckpointSequence ||
		cursor.EventSequence != fixture.cursorUpdated.EventSequence {
		t.Fatalf("cursor = %+v, want %+v", cursor, fixture.cursorUpdated)
	}
	if cursor.UpdatedAt.IsZero() {
		t.Fatal("cursor UpdatedAt = zero, want store-managed timestamp")
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%T): %v", value, err)
	}
	return data
}
