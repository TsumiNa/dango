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
	// Close releases backend-owned resources, bounding the wait by ctx. The
	// SQLite and Postgres backends cap the underlying database close by ctx (a
	// cancelled or expired ctx returns its error while the close finishes in the
	// background); the markdown backend holds no such resources and ignores ctx.
	Close(ctx context.Context) error
}
