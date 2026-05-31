package persistence

import (
	"context"

	runnerpkg "github.com/tsumina/dango/runner"
	storepkg "github.com/tsumina/dango/store"
)

// Backend is the unified persistence surface used by orchestrator and runner.
//
// EventLogStore, RunnerStore, and SnapshotCursorStore are orchestrator-level
// durable sinks. WorkspaceRoot is the global workspace root that runners use
// with a path rule to derive their own per-runner workspace subdirectory.
type Backend interface {
	// EventLogStore returns the request-stream event store.
	EventLogStore() storepkg.EventLogStore
	// RunnerStore returns the runner record store.
	RunnerStore() runnerpkg.RunnerStore
	// SnapshotCursorStore returns the describe replay cursor store.
	SnapshotCursorStore() storepkg.SnapshotCursorStore
	// WorkspaceRoot returns the global root under which runners allocate their
	// per-runner workspace via a path rule.
	WorkspaceRoot() string
	// Close releases backend-owned resources. ctx is intended to bound graceful
	// shutdown, but current backends (SQLite, Postgres, Markdown) ignore it and
	// close synchronously — see docs/persistence-close-ctx-memo.md. Callers
	// should still pass a real context so the contract holds once a backend
	// honors it.
	Close(ctx context.Context) error
}
