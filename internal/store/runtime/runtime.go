package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	persistencepkg "github.com/tsumina/dango/internal/engine/runner/persistence"
	storepkg "github.com/tsumina/dango/internal/store"
	sqlitepkg "github.com/tsumina/dango/internal/store/sqlite"
)

// Config controls how startup-owned persistence is opened.
type Config struct {
	// SQLitePath selects the durable SQLite backend. When empty, Open creates a
	// process-lifetime temporary JSON fallback and removes it during Close.
	SQLitePath string
}

// DefaultConfig returns the default startup persistence configuration.
func DefaultConfig() Config {
	return Config{}
}

// Persistence groups the startup-owned backend used by the orchestrator.
type Persistence struct {
	backend      persistencepkg.Backend
	sqliteStore  *sqlitepkg.Store
	rootDir      string
	closeBackend func(context.Context) error
}

// Open creates the startup-owned persistence bundle described by cfg.
//
// When cfg.SQLitePath is empty, Open creates a temporary JSON fallback rooted
// under the system temp directory. Otherwise it opens the configured SQLite
// store and exposes the SQLite-backed event log, runner, and cursor stores.
func Open(cfg Config) (*Persistence, error) {
	if strings.TrimSpace(cfg.SQLitePath) != "" {
		return openSQLitePersistence(cfg.SQLitePath)
	}
	return openJSONFallbackPersistence()
}

// EventLogStore returns the configured request event-log store.
func (p *Persistence) EventLogStore() storepkg.EventLogStore {
	if p == nil || p.backend == nil {
		return nil
	}
	return p.backend.EventLogStore()
}

// RunnerStore returns the configured runner checkpoint store.
func (p *Persistence) RunnerStore() runnerpkg.RunnerStore {
	if p == nil || p.backend == nil {
		return nil
	}
	return p.backend.RunnerStore()
}

// SnapshotCursorStore returns the configured describe snapshot cursor store.
func (p *Persistence) SnapshotCursorStore() storepkg.SnapshotCursorStore {
	if p == nil || p.backend == nil {
		return nil
	}
	return p.backend.SnapshotCursorStore()
}

// Backend returns the unified persistence backend used by orchestrator.
func (p *Persistence) Backend() persistencepkg.Backend {
	if p == nil {
		return nil
	}
	return p.backend
}

// RootDir returns the temporary fallback root directory, or the empty string
// when the persistence bundle uses a configured durable backend.
func (p *Persistence) RootDir() string {
	if p == nil {
		return ""
	}
	return p.rootDir
}

// Close releases the persistence bundle and removes any temporary fallback
// directory created by Open.
func (p *Persistence) Close() error {
	if p == nil {
		return nil
	}
	var errs []error
	if p.closeBackend != nil {
		if err := p.closeBackend(context.Background()); err != nil {
			errs = append(errs, err)
		}
	}
	if p.sqliteStore != nil {
		if err := p.sqliteStore.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if p.rootDir != "" {
		if err := os.RemoveAll(p.rootDir); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

func openSQLitePersistence(path string) (*Persistence, error) {
	dbStore, err := sqlitepkg.Open(path)
	if err != nil {
		return nil, fmt.Errorf("runtime persistence open sqlite %q: %w", path, err)
	}
	markdownRoot := filepath.Join(filepath.Dir(path), "workspace")
	markdownBackend, err := persistencepkg.NewMarkdownBackend(markdownRoot)
	if err != nil {
		_ = dbStore.Close()
		return nil, fmt.Errorf("runtime persistence open markdown workspace backend: %w", err)
	}
	backend := &compositeBackend{
		eventLogStore:     sqlitepkg.NewStreamStore(dbStore),
		runnerStore:       sqlitepkg.NewRunnerStore(dbStore),
		snapshotCursor:    sqlitepkg.NewSnapshotCursorStore(dbStore),
		workspaceRootPath: markdownBackend.WorkspaceRoot(),
	}
	return &Persistence{
		backend:      backend,
		sqliteStore:  dbStore,
		closeBackend: markdownBackend.Close,
	}, nil
}

func openJSONFallbackPersistence() (*Persistence, error) {
	root, err := os.MkdirTemp("", "dango-runtime-persistence-*")
	if err != nil {
		return nil, fmt.Errorf("runtime persistence create temp root: %w", err)
	}
	cleanup := func(wrap error) (*Persistence, error) {
		_ = os.RemoveAll(root)
		return nil, wrap
	}
	backend, err := persistencepkg.NewMarkdownBackend(root)
	if err != nil {
		return cleanup(fmt.Errorf("runtime persistence open markdown backend: %w", err))
	}
	return &Persistence{
		backend:      backend,
		rootDir:      root,
		closeBackend: backend.Close,
	}, nil
}

type compositeBackend struct {
	eventLogStore     storepkg.EventLogStore
	runnerStore       runnerpkg.RunnerStore
	snapshotCursor    storepkg.SnapshotCursorStore
	workspaceRootPath string
}

func (c *compositeBackend) EventLogStore() storepkg.EventLogStore { return c.eventLogStore }
func (c *compositeBackend) RunnerStore() runnerpkg.RunnerStore {
	return c.runnerStore
}
func (c *compositeBackend) SnapshotCursorStore() storepkg.SnapshotCursorStore {
	return c.snapshotCursor
}
func (c *compositeBackend) WorkspaceRoot() string       { return c.workspaceRootPath }
func (c *compositeBackend) Close(context.Context) error { return nil }
