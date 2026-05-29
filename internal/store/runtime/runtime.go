package runtime

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"sync"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	persistencepkg "github.com/tsumina/dango/internal/engine/runner/persistence"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	storepkg "github.com/tsumina/dango/internal/store"
)

// Config controls how startup-owned persistence is opened.
type Config struct {
	// SQLitePath selects the durable SQLite backend. When empty, Open creates a
	// process-lifetime temporary JSON fallback and removes it during Close.
	SQLitePath string
	// SQLiteMarkdownMirror enables JSON/JSONL markdown mirror stores alongside
	// SQLite when SQLitePath is configured. Reads still use SQLite.
	SQLiteMarkdownMirror bool
	// PostgresDSN selects the durable Postgres backend. When set, callers must
	// also provide PostgresWorkspaceRoot.
	PostgresDSN string
	// PostgresWorkspaceRoot is the global workspace root used by runner path
	// rules when PostgresDSN is configured.
	PostgresWorkspaceRoot string
	// PostgresMarkdownMirror enables JSON/JSONL markdown mirror stores
	// alongside Postgres when PostgresDSN is configured. Reads still use
	// Postgres.
	PostgresMarkdownMirror bool
}

// Persistence groups the startup-owned backend used by the orchestrator.
type Persistence struct {
	backend persistencepkg.Backend
	rootDir string
}

// Open creates the startup-owned persistence bundle described by cfg.
//
// Open chooses one durable backend when configured. SQLite uses cfg.SQLitePath
// while Postgres uses cfg.PostgresDSN with cfg.PostgresWorkspaceRoot. When
// neither backend is configured, Open creates a temporary JSON fallback rooted
// under the system temp directory.
func Open(cfg Config) (*Persistence, error) {
	hasSQLite := strings.TrimSpace(cfg.SQLitePath) != ""
	hasPostgres := strings.TrimSpace(cfg.PostgresDSN) != ""
	if hasSQLite && hasPostgres {
		return nil, fmt.Errorf("runtime persistence only supports one durable backend at a time")
	}
	if hasPostgres {
		if strings.TrimSpace(cfg.PostgresWorkspaceRoot) == "" {
			return nil, fmt.Errorf("runtime persistence postgres requires PostgresWorkspaceRoot")
		}
		if cfg.PostgresMarkdownMirror {
			return openPostgresCompositePersistence(cfg.PostgresDSN, cfg.PostgresWorkspaceRoot)
		}
		return openPostgresPersistence(cfg.PostgresDSN, cfg.PostgresWorkspaceRoot)
	}
	if strings.TrimSpace(cfg.SQLitePath) != "" {
		if cfg.SQLiteMarkdownMirror {
			return openSQLiteCompositePersistence(cfg.SQLitePath)
		}
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
	if p.backend != nil {
		if err := p.backend.Close(context.Background()); err != nil {
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
	backend, err := persistencepkg.NewSQLiteBackend(path)
	if err != nil {
		return nil, fmt.Errorf("runtime persistence open sqlite %q: %w", path, err)
	}
	return &Persistence{backend: backend}, nil
}

func openSQLiteCompositePersistence(path string) (*Persistence, error) {
	sqliteBackend, err := persistencepkg.NewSQLiteBackend(path)
	if err != nil {
		return nil, fmt.Errorf("runtime persistence open sqlite %q: %w", path, err)
	}
	markdownRoot := filepath.Dir(path)
	markdownBackend, err := persistencepkg.NewMarkdownBackend(markdownRoot)
	if err != nil {
		_ = sqliteBackend.Close(context.Background())
		return nil, fmt.Errorf("runtime persistence open markdown mirror backend: %w", err)
	}
	backend := &compositeBackend{
		eventLogStore: &compositeEventLogStore{
			primary: sqliteBackend.EventLogStore(),
			mirror:  markdownBackend.EventLogStore(),
		},
		runnerStore: &compositeRunnerStore{
			primary: sqliteBackend.RunnerStore(),
			mirror:  markdownBackend.RunnerStore(),
		},
		snapshotCursorStore: &compositeSnapshotCursorStore{
			primary: sqliteBackend.SnapshotCursorStore(),
			mirror:  markdownBackend.SnapshotCursorStore(),
		},
		workspaceRoot: sqliteBackend.WorkspaceRoot(),
		closeOnce: sync.OnceValue(func() error {
			return errors.Join(sqliteBackend.Close(context.Background()), markdownBackend.Close(context.Background()))
		}),
	}
	return &Persistence{backend: backend}, nil
}

func openPostgresPersistence(dsn string, workspaceRoot string) (*Persistence, error) {
	backend, err := persistencepkg.NewPostgresBackend(dsn, workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("runtime persistence open postgres: %w", err)
	}
	return &Persistence{backend: backend}, nil
}

func openPostgresCompositePersistence(dsn string, workspaceRoot string) (*Persistence, error) {
	postgresBackend, err := persistencepkg.NewPostgresBackend(dsn, workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("runtime persistence open postgres: %w", err)
	}
	markdownRoot := filepath.Join(postgresBackend.WorkspaceRoot(), ".markdown-mirror")
	markdownBackend, err := persistencepkg.NewMarkdownBackend(markdownRoot)
	if err != nil {
		_ = postgresBackend.Close(context.Background())
		return nil, fmt.Errorf("runtime persistence open markdown mirror backend: %w", err)
	}
	backend := &compositeBackend{
		eventLogStore: &compositeEventLogStore{
			primary: postgresBackend.EventLogStore(),
			mirror:  markdownBackend.EventLogStore(),
		},
		runnerStore: &compositeRunnerStore{
			primary: postgresBackend.RunnerStore(),
			mirror:  markdownBackend.RunnerStore(),
		},
		snapshotCursorStore: &compositeSnapshotCursorStore{
			primary: postgresBackend.SnapshotCursorStore(),
			mirror:  markdownBackend.SnapshotCursorStore(),
		},
		workspaceRoot: postgresBackend.WorkspaceRoot(),
		closeOnce: sync.OnceValue(func() error {
			return errors.Join(postgresBackend.Close(context.Background()), markdownBackend.Close(context.Background()))
		}),
	}
	return &Persistence{backend: backend}, nil
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
		backend: backend,
		rootDir: root,
	}, nil
}

// compositeBackend combines durable-backed stores with a filesystem workspace
// root used by runner markdown/workspace artifacts.
//
// The backend owns both the durable store and markdown workspace backend close
// lifecycle via closeOnce. Close joins those shutdown paths exactly once, so
// callers may close either Persistence or the exposed Backend safely.
type compositeBackend struct {
	eventLogStore       storepkg.EventLogStore
	runnerStore         runnerpkg.RunnerStore
	snapshotCursorStore storepkg.SnapshotCursorStore
	workspaceRoot       string
	closeOnce           func() error
}

func (c *compositeBackend) EventLogStore() storepkg.EventLogStore { return c.eventLogStore }
func (c *compositeBackend) RunnerStore() runnerpkg.RunnerStore {
	return c.runnerStore
}
func (c *compositeBackend) SnapshotCursorStore() storepkg.SnapshotCursorStore {
	return c.snapshotCursorStore
}
func (c *compositeBackend) WorkspaceRoot() string { return c.workspaceRoot }
func (c *compositeBackend) Close(context.Context) error {
	if c == nil || c.closeOnce == nil {
		return nil
	}
	return c.closeOnce()
}

type compositeEventLogStore struct {
	primary storepkg.EventLogStore
	mirror  storepkg.EventLogStore
}

func (s *compositeEventLogStore) AppendEvent(ctx context.Context, event streampkg.Event) error {
	if s == nil || s.primary == nil || s.mirror == nil {
		return fmt.Errorf("runtime persistence composite event log store is not configured")
	}
	if err := s.primary.AppendEvent(ctx, event); err != nil {
		return fmt.Errorf("runtime persistence append primary event log: %w", err)
	}
	// Mirror persistence is best-effort: once the primary durable write succeeds
	// we must still return success to avoid retry ambiguity on unique keys.
	_ = s.mirror.AppendEvent(ctx, event)
	return nil
}

func (s *compositeEventLogStore) LoadEvents(ctx context.Context, scope streampkg.Scope, from uint64, filter streampkg.Filter) ([]streampkg.Event, error) {
	if s == nil || s.primary == nil {
		return nil, fmt.Errorf("runtime persistence composite event log primary store is not configured")
	}
	return s.primary.LoadEvents(ctx, scope, from, filter)
}

type compositeRunnerStore struct {
	primary runnerpkg.RunnerStore
	mirror  runnerpkg.RunnerStore

	appendLocks [compositeRunnerLockCount]sync.Mutex
}

const compositeRunnerLockCount = 64

func (s *compositeRunnerStore) Append(ctx context.Context, runnerID string, rec *runnerpkg.RunnerRecord) (int64, error) {
	if s == nil || s.primary == nil || s.mirror == nil {
		return 0, fmt.Errorf("runtime persistence composite runner store is not configured")
	}
	unlock := s.lock(runnerID)
	defer unlock()

	seq, err := s.primary.Append(ctx, runnerID, rec)
	if err != nil {
		return 0, fmt.Errorf("runtime persistence append primary runner record: %w", err)
	}
	rec.Seq = seq
	mirrorRec := cloneRunnerRecord(rec)
	if _, err := s.mirror.Append(ctx, runnerID, mirrorRec); err != nil {
		return 0, fmt.Errorf("runtime persistence append markdown mirror runner record: %w", err)
	}
	return seq, nil
}

func (s *compositeRunnerStore) Load(ctx context.Context, runnerID string) ([]runnerpkg.RunnerRecord, error) {
	if s == nil || s.primary == nil {
		return nil, fmt.Errorf("runtime persistence composite runner primary store is not configured")
	}
	return s.primary.Load(ctx, runnerID)
}

func (s *compositeRunnerStore) Delete(ctx context.Context, runnerID string) error {
	if s == nil || s.primary == nil || s.mirror == nil {
		return fmt.Errorf("runtime persistence composite runner store is not configured")
	}
	if err := s.primary.Delete(ctx, runnerID); err != nil {
		return fmt.Errorf("runtime persistence delete primary runner record: %w", err)
	}
	if err := s.mirror.Delete(ctx, runnerID); err != nil {
		return fmt.Errorf("runtime persistence delete markdown mirror runner record: %w", err)
	}
	return nil
}

func (s *compositeRunnerStore) lock(runnerID string) func() {
	lock := &s.appendLocks[compositeRunnerLockIndex(runnerID)]
	lock.Lock()
	return lock.Unlock
}

// compositeRunnerLockIndex hashes a runner id into the fixed stripe set used to
// serialize composite append operations for that runner.
func compositeRunnerLockIndex(runnerID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(runnerID))
	return h.Sum32() % uint32(compositeRunnerLockCount)
}

func cloneRunnerRecord(rec *runnerpkg.RunnerRecord) *runnerpkg.RunnerRecord {
	clone := *rec
	if rec.Event != nil {
		eventClone := *rec.Event
		if rec.Event.DataJSON != nil {
			eventClone.DataJSON = append([]byte(nil), rec.Event.DataJSON...)
		}
		clone.Event = &eventClone
	}
	return &clone
}

type compositeSnapshotCursorStore struct {
	primary storepkg.SnapshotCursorStore
	mirror  storepkg.SnapshotCursorStore
}

func (s *compositeSnapshotCursorStore) SaveCursor(ctx context.Context, cursor storepkg.SnapshotCursor) error {
	if s == nil || s.primary == nil || s.mirror == nil {
		return fmt.Errorf("runtime persistence composite snapshot cursor store is not configured")
	}
	if err := s.primary.SaveCursor(ctx, cursor); err != nil {
		return fmt.Errorf("runtime persistence save primary snapshot cursor: %w", err)
	}
	if err := s.mirror.SaveCursor(ctx, cursor); err != nil {
		return fmt.Errorf("runtime persistence save markdown mirror snapshot cursor: %w", err)
	}
	return nil
}

func (s *compositeSnapshotCursorStore) LoadCursor(ctx context.Context, requestID string) (storepkg.SnapshotCursor, error) {
	if s == nil || s.primary == nil {
		return storepkg.SnapshotCursor{}, fmt.Errorf("runtime persistence composite snapshot cursor primary store is not configured")
	}
	return s.primary.LoadCursor(ctx, requestID)
}
