package persistence

import (
	"context"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	storepkg "github.com/tsumina/dango/internal/store"
)

type noneBackend struct{}

// None returns a backend that disables persistence.
func None() Backend {
	return &noneBackend{}
}

func (n *noneBackend) EventLogStore() storepkg.EventLogStore { return nil }

func (n *noneBackend) RunnerStore() runnerpkg.RunnerStore { return nil }

func (n *noneBackend) SnapshotCursorStore() storepkg.SnapshotCursorStore { return nil }

func (n *noneBackend) WorkspaceRoot() string { return "" }

func (n *noneBackend) Close(context.Context) error { return nil }
