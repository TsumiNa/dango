package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runnerpkg "github.com/tsumina/dango/runner"
	persistencepkg "github.com/tsumina/dango/runner/persistence"
	storepkg "github.com/tsumina/dango/store"
	sqlitepkg "github.com/tsumina/dango/store/internal/sqlite"
)

// SQLiteBackend persists orchestrator/runner state in SQLite and provides a
// filesystem workspace root for runner-managed artifacts.
type SQLiteBackend struct {
	dbStore        *sqlitepkg.Store
	eventLogStore  storepkg.EventLogStore
	runnerStore    runnerpkg.RunnerStore
	snapshotCursor storepkg.SnapshotCursorStore
	workspaceRoot  string
}

var _ persistencepkg.Backend = (*SQLiteBackend)(nil)

// NewSQLiteBackend creates a SQLite-backed persistence backend rooted at path.
//
// WorkspaceRoot is set to a sibling "workspace" directory next to the
// configured database file.
func NewSQLiteBackend(path string) (*SQLiteBackend, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("runner/persistence: sqlite backend path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("runner/persistence: resolve sqlite backend path: %w", err)
	}
	dbStore, err := sqlitepkg.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("runner/persistence: open sqlite backend: %w", err)
	}
	workspaceRoot := filepath.Join(filepath.Dir(absPath), "workspace")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		_ = dbStore.Close()
		return nil, fmt.Errorf("runner/persistence: create sqlite workspace root: %w", err)
	}
	return &SQLiteBackend{
		dbStore:        dbStore,
		eventLogStore:  sqlitepkg.NewStreamStore(dbStore),
		runnerStore:    sqlitepkg.NewRunnerStore(dbStore),
		snapshotCursor: sqlitepkg.NewSnapshotCursorStore(dbStore),
		workspaceRoot:  workspaceRoot,
	}, nil
}

func (s *SQLiteBackend) EventLogStore() storepkg.EventLogStore {
	if s == nil {
		return nil
	}
	return s.eventLogStore
}

func (s *SQLiteBackend) RunnerStore() runnerpkg.RunnerStore {
	if s == nil {
		return nil
	}
	return s.runnerStore
}

func (s *SQLiteBackend) SnapshotCursorStore() storepkg.SnapshotCursorStore {
	if s == nil {
		return nil
	}
	return s.snapshotCursor
}

func (s *SQLiteBackend) WorkspaceRoot() string {
	if s == nil {
		return ""
	}
	return s.workspaceRoot
}

func (s *SQLiteBackend) Close(context.Context) error {
	if s == nil || s.dbStore == nil {
		return nil
	}
	return s.dbStore.Close()
}
