package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
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

// Persistence groups the startup-owned stores used by the orchestrator.
type Persistence struct {
	eventLogStore       storepkg.EventLogStore
	runnerStore         runnerpkg.RunnerStore
	snapshotCursorStore storepkg.SnapshotCursorStore
	sqliteStore         *sqlitepkg.Store
	rootDir             string
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
	if p == nil {
		return nil
	}
	return p.eventLogStore
}

// RunnerStore returns the configured runner checkpoint store.
func (p *Persistence) RunnerStore() runnerpkg.RunnerStore {
	if p == nil {
		return nil
	}
	return p.runnerStore
}

// SnapshotCursorStore returns the configured describe snapshot cursor store.
func (p *Persistence) SnapshotCursorStore() storepkg.SnapshotCursorStore {
	if p == nil {
		return nil
	}
	return p.snapshotCursorStore
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
	return &Persistence{
		eventLogStore:       sqlitepkg.NewStreamStore(dbStore),
		runnerStore:         sqlitepkg.NewRunnerStore(dbStore),
		snapshotCursorStore: sqlitepkg.NewSnapshotCursorStore(dbStore),
		sqliteStore:         dbStore,
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
	eventLogStore, err := storepkg.NewJSONEventLogStore(filepath.Join(root, "event-log"))
	if err != nil {
		return cleanup(fmt.Errorf("runtime persistence open JSON event log: %w", err))
	}
	runnerStore, err := runnerpkg.NewJSONRunnerStore(filepath.Join(root, "runner-log"))
	if err != nil {
		return cleanup(fmt.Errorf("runtime persistence open JSON runner log: %w", err))
	}
	cursorStore, err := storepkg.NewJSONSnapshotCursorStore(filepath.Join(root, "snapshot-cursor"))
	if err != nil {
		return cleanup(fmt.Errorf("runtime persistence open JSON snapshot cursor: %w", err))
	}
	return &Persistence{
		eventLogStore:       eventLogStore,
		runnerStore:         runnerStore,
		snapshotCursorStore: cursorStore,
		rootDir:             root,
	}, nil
}