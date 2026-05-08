package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenAppliesEmbeddedMigrations(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}()

	var version int
	var dirty bool
	if err := store.db.QueryRow(`SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&version, &dirty); err != nil {
		t.Fatalf("query schema_migrations error = %v", err)
	}
	if version != 1 {
		t.Fatalf("schema version = %d, want 1", version)
	}
	if dirty {
		t.Fatal("schema dirty = true, want false")
	}

	tool := ToolRecord{
		Name:       "demo-tool",
		Image:      "host://demo-tool",
		ConfigJSON: `{"mode":"demo"}`,
	}
	if err := store.UpsertTool(context.Background(), tool); err != nil {
		t.Fatalf("UpsertTool() error = %v", err)
	}

	got, err := store.GetTool(context.Background(), tool.Name)
	if err != nil {
		t.Fatalf("GetTool() error = %v", err)
	}
	if got.Name != tool.Name || got.Image != tool.Image || got.ConfigJSON != tool.ConfigJSON {
		t.Fatalf("GetTool() = %#v, want name=%q image=%q config=%q", got, tool.Name, tool.Image, tool.ConfigJSON)
	}
	if got.Registered == "" {
		t.Fatal("GetTool().Registered = empty, want sqlite timestamp")
	}
}

func TestTaskCreateGetRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	ctx := context.Background()
	task := TaskRecord{
		ID:      "task-1",
		Status:  "pending",
		Request: "summarize the project",
	}
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ID != task.ID {
		t.Errorf("ID = %q, want %q", got.ID, task.ID)
	}
	if got.Status != task.Status {
		t.Errorf("Status = %q, want %q", got.Status, task.Status)
	}
	if got.Request != task.Request {
		t.Errorf("Request = %q, want %q", got.Request, task.Request)
	}
	if got.Created == "" {
		t.Error("Created is empty, want sqlite timestamp")
	}
	if got.Updated == "" {
		t.Error("Updated is empty, want sqlite timestamp")
	}
}

func TestTaskGetMissingReturnsNotFound(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	_, err = store.GetTask(context.Background(), "no-such-task")
	if !store.IsNotFound(err) {
		t.Fatalf("GetTask missing err = %v, want not-found", err)
	}
}

func TestTaskUpdateStatusRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	ctx := context.Background()
	if err := store.CreateTask(ctx, TaskRecord{ID: "t2", Status: "pending"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := store.UpdateTaskStatus(ctx, "t2", "running"); err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}

	got, err := store.GetTask(ctx, "t2")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("Status = %q, want running", got.Status)
	}
}

func TestTaskUpdateStatusMissingReturnsNotFound(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	err = store.UpdateTaskStatus(context.Background(), "no-such", "running")
	if !store.IsNotFound(err) {
		t.Fatalf("UpdateTaskStatus missing err = %v, want not-found", err)
	}
}

func TestTaskUpdatePlanRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	ctx := context.Background()
	if err := store.CreateTask(ctx, TaskRecord{ID: "t3", Status: "pending"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	dagJSON := `{"nodes":[{"id":"n1"}]}`
	if err := store.UpdateTaskPlan(ctx, "t3", "planned", dagJSON); err != nil {
		t.Fatalf("UpdateTaskPlan: %v", err)
	}

	got, err := store.GetTask(ctx, "t3")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != "planned" {
		t.Errorf("Status = %q, want planned", got.Status)
	}
	if got.DAGJSON != dagJSON {
		t.Errorf("DAGJSON = %q, want %q", got.DAGJSON, dagJSON)
	}
}

func TestEdgeUpsertRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	ctx := context.Background()
	mustCreateTaskAndTool(t, store, ctx)

	edge := EdgeRecord{
		ID:       "edge-1",
		TaskID:   "task-fixture",
		ToolName: "tool-fixture",
		Status:   "pending",
	}
	if err := store.UpsertEdge(ctx, edge); err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}

	var gotStatus string
	if err := store.db.QueryRowContext(ctx,
		`SELECT status FROM edges WHERE id = ?`, edge.ID,
	).Scan(&gotStatus); err != nil {
		t.Fatalf("query edge: %v", err)
	}
	if gotStatus != edge.Status {
		t.Errorf("edge status = %q, want %q", gotStatus, edge.Status)
	}
}

func TestEdgeUpdateResult(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	ctx := context.Background()
	mustCreateTaskAndTool(t, store, ctx)

	if err := store.UpsertEdge(ctx, EdgeRecord{
		ID:       "edge-2",
		TaskID:   "task-fixture",
		ToolName: "tool-fixture",
		Status:   "running",
	}); err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}

	finished := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	if err := store.UpdateEdgeResult(ctx, "edge-2", "completed", `handoff: true`, finished); err != nil {
		t.Fatalf("UpdateEdgeResult: %v", err)
	}

	var gotStatus string
	if err := store.db.QueryRowContext(ctx,
		`SELECT status FROM edges WHERE id = ?`, "edge-2",
	).Scan(&gotStatus); err != nil {
		t.Fatalf("query edge after update: %v", err)
	}
	if gotStatus != "completed" {
		t.Errorf("edge status = %q, want completed", gotStatus)
	}
}

func TestEdgeUpdateResultMissingReturnsNotFound(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	err = store.UpdateEdgeResult(context.Background(), "no-such-edge", "completed", "", time.Now())
	if !store.IsNotFound(err) {
		t.Fatalf("UpdateEdgeResult missing err = %v, want not-found", err)
	}
}

func TestLogInsertRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	ctx := context.Background()
	mustCreateTaskAndTool(t, store, ctx)
	if err := store.UpsertEdge(ctx, EdgeRecord{
		ID:       "edge-log",
		TaskID:   "task-fixture",
		ToolName: "tool-fixture",
		Status:   "running",
	}); err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}

	if err := store.InsertLog(ctx, "edge-log", "info", "execution started"); err != nil {
		t.Fatalf("InsertLog: %v", err)
	}
	if err := store.InsertLog(ctx, "edge-log", "error", "something went wrong"); err != nil {
		t.Fatalf("InsertLog second: %v", err)
	}

	var count int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM logs WHERE edge_id = ?`, "edge-log",
	).Scan(&count); err != nil {
		t.Fatalf("query log count: %v", err)
	}
	if count != 2 {
		t.Errorf("log count = %d, want 2", count)
	}

	var level, message string
	if err := store.db.QueryRowContext(ctx,
		`SELECT level, message FROM logs WHERE edge_id = ? ORDER BY id ASC LIMIT 1`, "edge-log",
	).Scan(&level, &message); err != nil {
		t.Fatalf("query first log: %v", err)
	}
	if level != "info" || message != "execution started" {
		t.Errorf("first log = {%q, %q}, want {info, execution started}", level, message)
	}
}

// mustCreateTaskAndTool inserts a reusable task and tool fixture so edge and log
// tests can satisfy the NOT NULL foreign-key columns without repeating setup.
func mustCreateTaskAndTool(t *testing.T, store *Store, ctx context.Context) {
	t.Helper()
	if err := store.UpsertTool(ctx, ToolRecord{
		Name:       "tool-fixture",
		Image:      "host://tool-fixture",
		ConfigJSON: `{}`,
	}); err != nil {
		t.Fatalf("UpsertTool fixture: %v", err)
	}
	if err := store.CreateTask(ctx, TaskRecord{
		ID:     "task-fixture",
		Status: "pending",
	}); err != nil {
		t.Fatalf("CreateTask fixture: %v", err)
	}
}

// Ensure sql.ErrNoRows is recognized as not-found.
func TestIsNotFoundForSQLNoRows(t *testing.T) {
	t.Parallel()

	store, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	if !store.IsNotFound(sql.ErrNoRows) {
		t.Error("IsNotFound(sql.ErrNoRows) = false, want true")
	}
	if store.IsNotFound(nil) {
		t.Error("IsNotFound(nil) = true, want false")
	}
}
