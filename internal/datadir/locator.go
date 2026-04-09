package datadir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	registryDirName        = "registry"
	tasksDirName           = "tasks"
	dbFileName             = "dango.db"
	publicHandoffFileName  = "handoff.md"
	privateHandoffFileName = "_handoff.md"
)

// Locator resolves the canonical on-disk paths used by an orchestrator data
// directory.
//
// The zero value is not usable. Construct [Locator] values with [New] so Root
// is normalized to an absolute path.
type Locator struct {
	// Root is the absolute path to the orchestrator data directory.
	Root string
}

// New validates root and returns a [Locator] rooted at its absolute filesystem
// path.
func New(root string) (*Locator, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("data dir is required")
	}

	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve data dir %q: %w", root, err)
	}

	return &Locator{Root: absoluteRoot}, nil
}

// Ensure creates the top-level directories required by the data directory.
func (l *Locator) Ensure() error {
	for _, dir := range []string{l.Root, l.RegistryDir(), l.TasksDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %q: %w", dir, err)
		}
	}

	return nil
}

// DBPath returns the SQLite database path for the data directory.
func (l *Locator) DBPath() string {
	return filepath.Join(l.Root, dbFileName)
}

// RegistryDir returns the directory containing registered tool definitions.
func (l *Locator) RegistryDir() string {
	return filepath.Join(l.Root, registryDirName)
}

// TasksDir returns the directory containing per-task execution data.
func (l *Locator) TasksDir() string {
	return filepath.Join(l.Root, tasksDirName)
}

// ToolDir returns the directory used to store one registered tool.
func (l *Locator) ToolDir(name string) string {
	return filepath.Join(l.RegistryDir(), safeComponent(name))
}

// ToolSpecPath returns the stored tool.yaml path for a registered tool.
func (l *Locator) ToolSpecPath(name string) string {
	return filepath.Join(l.ToolDir(name), "tool.yaml")
}

// ToolOverridePath returns the override.yaml path for a registered tool.
func (l *Locator) ToolOverridePath(name string) string {
	return filepath.Join(l.ToolDir(name), "override.yaml")
}

// ToolMergedPath returns the merged.yaml path for a registered tool.
func (l *Locator) ToolMergedPath(name string) string {
	return filepath.Join(l.ToolDir(name), "merged.yaml")
}

// TaskDir returns the root directory for one task.
func (l *Locator) TaskDir(taskID string) string {
	return filepath.Join(l.TasksDir(), taskID)
}

// TaskRequestPath returns the task.md path for a task.
func (l *Locator) TaskRequestPath(taskID string) string {
	return filepath.Join(l.TaskDir(taskID), "task.md")
}

// TaskMetadataPath returns the task metadata path for a task.
func (l *Locator) TaskMetadataPath(taskID string) string {
	return filepath.Join(l.TaskDir(taskID), "meta.json")
}

// TaskEventsPath returns the append-only event log path for a task.
func (l *Locator) TaskEventsPath(taskID string) string {
	return filepath.Join(l.TaskDir(taskID), "events.jsonl")
}

// TaskResultPath returns the result.md path for a task.
func (l *Locator) TaskResultPath(taskID string) string {
	return filepath.Join(l.TaskDir(taskID), "result.md")
}

// EdgeDir returns the directory for one planned edge within a task.
func (l *Locator) EdgeDir(taskID, edgeID string) string {
	return filepath.Join(l.TaskDir(taskID), "edges", edgeID)
}

// EdgeSubTaskPath returns the sub-task markdown path for an edge.
func (l *Locator) EdgeSubTaskPath(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), "sub-task.md")
}

// EdgeOutputDir returns the output directory for an edge.
func (l *Locator) EdgeOutputDir(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), "output")
}

// EdgePrivateOutputDir returns the private downstream-only output directory for an edge.
func (l *Locator) EdgePrivateOutputDir(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), "_output")
}

// EdgeScratchInputDir returns the empty scratch input directory for root edges.
func (l *Locator) EdgeScratchInputDir(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), ".input-empty")
}

// EdgeMergedInputDir returns the directory used to merge multiple dependency inputs.
func (l *Locator) EdgeMergedInputDir(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), ".input-merged")
}

// EdgePublicHandoffPath returns the public handoff.md path for an edge.
func (l *Locator) EdgePublicHandoffPath(taskID, edgeID string) string {
	return filepath.Join(l.EdgeOutputDir(taskID, edgeID), publicHandoffFileName)
}

// EdgePrivateHandoffPath returns the private _handoff.md path for an edge.
func (l *Locator) EdgePrivateHandoffPath(taskID, edgeID string) string {
	return filepath.Join(l.EdgePrivateOutputDir(taskID, edgeID), privateHandoffFileName)
}

// EnsureToolDir creates the directory used to persist one registered tool.
func (l *Locator) EnsureToolDir(name string) error {
	return os.MkdirAll(l.ToolDir(name), 0o755)
}

// EnsureTaskDir creates the root directory for a task.
func (l *Locator) EnsureTaskDir(taskID string) error {
	return os.MkdirAll(l.TaskDir(taskID), 0o755)
}

// EnsureEdgeDir creates the directories needed to execute one edge.
func (l *Locator) EnsureEdgeDir(taskID, edgeID string) error {
	paths := []string{
		l.EdgeDir(taskID, edgeID),
		l.EdgeOutputDir(taskID, edgeID),
		l.EdgePrivateOutputDir(taskID, edgeID),
	}

	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("create edge directory %q: %w", p, err)
		}
	}

	return nil
}

func safeComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}

	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}

	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "unknown"
	}

	return result
}
