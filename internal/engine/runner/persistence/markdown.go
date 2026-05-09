package persistence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	storepkg "github.com/tsumina/dango/internal/store"
)

// MarkdownBackend persists orchestrator/runner state in JSON/JSONL files and
// provides a filesystem workspace root for runner-managed markdown artifacts.
type MarkdownBackend struct {
	root             string
	eventLogStore    storepkg.EventLogStore
	runnerStore      runnerpkg.RunnerStore
	snapshotCursor   storepkg.SnapshotCursorStore
	workspaceRootDir string
}

// NewMarkdownBackend creates a markdown/file-backed persistence backend rooted
// at root.
func NewMarkdownBackend(root string) (*MarkdownBackend, error) {
	if root == "" {
		return nil, fmt.Errorf("runner/persistence: markdown backend root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("runner/persistence: resolve markdown backend root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return nil, fmt.Errorf("runner/persistence: create markdown backend root: %w", err)
	}
	eventLogStore, err := storepkg.NewJSONEventLogStore(filepath.Join(absRoot, "event-log"))
	if err != nil {
		return nil, fmt.Errorf("runner/persistence: open markdown event log store: %w", err)
	}
	runnerStore, err := runnerpkg.NewJSONRunnerStore(filepath.Join(absRoot, "runner-log"))
	if err != nil {
		return nil, fmt.Errorf("runner/persistence: open markdown runner store: %w", err)
	}
	snapshotCursor, err := storepkg.NewJSONSnapshotCursorStore(filepath.Join(absRoot, "snapshot-cursor"))
	if err != nil {
		return nil, fmt.Errorf("runner/persistence: open markdown snapshot cursor store: %w", err)
	}
	workspaceRootDir := filepath.Join(absRoot, "workspace")
	if err := os.MkdirAll(workspaceRootDir, 0o755); err != nil {
		return nil, fmt.Errorf("runner/persistence: create markdown workspace root: %w", err)
	}
	return &MarkdownBackend{
		root:             absRoot,
		eventLogStore:    eventLogStore,
		runnerStore:      runnerStore,
		snapshotCursor:   snapshotCursor,
		workspaceRootDir: workspaceRootDir,
	}, nil
}

func (m *MarkdownBackend) EventLogStore() storepkg.EventLogStore {
	if m == nil {
		return nil
	}
	return m.eventLogStore
}

func (m *MarkdownBackend) RunnerStore() runnerpkg.RunnerStore {
	if m == nil {
		return nil
	}
	return m.runnerStore
}

func (m *MarkdownBackend) SnapshotCursorStore() storepkg.SnapshotCursorStore {
	if m == nil {
		return nil
	}
	return m.snapshotCursor
}

func (m *MarkdownBackend) WorkspaceRoot() string {
	if m == nil {
		return ""
	}
	return m.workspaceRootDir
}

func (m *MarkdownBackend) Close(context.Context) error { return nil }
