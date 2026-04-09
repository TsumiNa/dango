package sqlite

import (
	"context"
	"path/filepath"
	"testing"
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
