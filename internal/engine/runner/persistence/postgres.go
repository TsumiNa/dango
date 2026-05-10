package persistence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	storepkg "github.com/tsumina/dango/internal/store"
	postgrespkg "github.com/tsumina/dango/internal/store/postgres"
)

// PostgresBackend persists orchestrator/runner state in Postgres and provides
// a filesystem workspace root for runner-managed artifacts.
type PostgresBackend struct {
	dbStore        *postgrespkg.Store
	eventLogStore  storepkg.EventLogStore
	runnerStore    runnerpkg.RunnerStore
	snapshotCursor storepkg.SnapshotCursorStore
	workspaceRoot  string
}

// NewPostgresBackend creates a Postgres-backed persistence backend.
//
// workspaceRoot is the global workspace root used by the runner path rule.
func NewPostgresBackend(dsn string, workspaceRoot string) (*PostgresBackend, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("runner/persistence: postgres backend dsn is required")
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil, fmt.Errorf("runner/persistence: postgres backend workspace root is required")
	}
	absWorkspaceRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("runner/persistence: resolve postgres workspace root: %w", err)
	}
	if err := os.MkdirAll(absWorkspaceRoot, 0o755); err != nil {
		return nil, fmt.Errorf("runner/persistence: create postgres workspace root: %w", err)
	}
	dbStore, err := postgrespkg.Open(dsn)
	if err != nil {
		return nil, fmt.Errorf("runner/persistence: open postgres backend: %w", err)
	}
	return &PostgresBackend{
		dbStore:        dbStore,
		eventLogStore:  postgrespkg.NewStreamStore(dbStore),
		runnerStore:    postgrespkg.NewRunnerStore(dbStore),
		snapshotCursor: postgrespkg.NewSnapshotCursorStore(dbStore),
		workspaceRoot:  absWorkspaceRoot,
	}, nil
}

func (p *PostgresBackend) EventLogStore() storepkg.EventLogStore {
	if p == nil {
		return nil
	}
	return p.eventLogStore
}

func (p *PostgresBackend) RunnerStore() runnerpkg.RunnerStore {
	if p == nil {
		return nil
	}
	return p.runnerStore
}

func (p *PostgresBackend) SnapshotCursorStore() storepkg.SnapshotCursorStore {
	if p == nil {
		return nil
	}
	return p.snapshotCursor
}

func (p *PostgresBackend) WorkspaceRoot() string {
	if p == nil {
		return ""
	}
	return p.workspaceRoot
}

func (p *PostgresBackend) Close(context.Context) error {
	if p == nil || p.dbStore == nil {
		return nil
	}
	return p.dbStore.Close()
}
