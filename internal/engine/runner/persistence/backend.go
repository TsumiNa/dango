package persistence

import (
	"context"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	storepkg "github.com/tsumina/dango/internal/store"
)

// Backend is the unified persistence surface used by orchestrator and runner.
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
	// Close releases backend-owned resources.
	Close(context.Context) error
}
