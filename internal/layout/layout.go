package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	registryDirName = "registry"
	tasksDirName    = "tasks"
	dbFileName      = "dango.db"
	handoffFileName = "_handoff.md"
)

// Layout resolves the canonical on-disk paths used by an orchestrator data
// directory.
//
// The zero value is not usable. Construct Layout values with New so Root is
// normalized to an absolute path.
type Layout struct {
	// Root is the absolute path to the orchestrator data directory.
	Root string
}

// New validates root and returns a Layout rooted at its absolute filesystem
// path.
func New(root string) (*Layout, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("data dir is required")
	}

	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve data dir %q: %w", root, err)
	}

	return &Layout{Root: absoluteRoot}, nil
}

// Ensure creates the top-level directories required by the layout.
func (l *Layout) Ensure() error {
	for _, dir := range []string{l.Root, l.RegistryDir(), l.TasksDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %q: %w", dir, err)
		}
	}

	return nil
}

// DBPath returns the SQLite database path for the data directory.
func (l *Layout) DBPath() string {
	return filepath.Join(l.Root, dbFileName)
}

// RegistryDir returns the directory containing registered tool definitions.
func (l *Layout) RegistryDir() string {
	return filepath.Join(l.Root, registryDirName)
}

// TasksDir returns the directory containing per-task execution data.
func (l *Layout) TasksDir() string {
	return filepath.Join(l.Root, tasksDirName)
}

// ToolDir returns the directory used to store one registered tool.
func (l *Layout) ToolDir(name string) string {
	return filepath.Join(l.RegistryDir(), safeComponent(name))
}

// ToolSpecPath returns the stored tool.yaml path for a registered tool.
func (l *Layout) ToolSpecPath(name string) string {
	return filepath.Join(l.ToolDir(name), "tool.yaml")
}

// ToolOverridePath returns the override.yaml path for a registered tool.
func (l *Layout) ToolOverridePath(name string) string {
	return filepath.Join(l.ToolDir(name), "override.yaml")
}

// ToolMergedPath returns the merged.yaml path for a registered tool.
func (l *Layout) ToolMergedPath(name string) string {
	return filepath.Join(l.ToolDir(name), "merged.yaml")
}

// TaskDir returns the root directory for one task.
func (l *Layout) TaskDir(taskID string) string {
	return filepath.Join(l.TasksDir(), taskID)
}

// TaskRequestPath returns the task.md path for a task.
func (l *Layout) TaskRequestPath(taskID string) string {
	return filepath.Join(l.TaskDir(taskID), "task.md")
}

// TaskResultPath returns the result.md path for a task.
func (l *Layout) TaskResultPath(taskID string) string {
	return filepath.Join(l.TaskDir(taskID), "result.md")
}

// EdgeDir returns the directory for one planned edge within a task.
func (l *Layout) EdgeDir(taskID, edgeID string) string {
	return filepath.Join(l.TaskDir(taskID), "edges", edgeID)
}

// EdgeSubTaskPath returns the sub-task markdown path for an edge.
func (l *Layout) EdgeSubTaskPath(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), "sub-task.md")
}

// EdgeOutputDir returns the output directory for an edge.
func (l *Layout) EdgeOutputDir(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), "output")
}

// EdgeScratchInputDir returns the empty scratch input directory for root edges.
func (l *Layout) EdgeScratchInputDir(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), ".input-empty")
}

// EdgeHandoffPath returns the required _handoff.md path for an edge output.
func (l *Layout) EdgeHandoffPath(taskID, edgeID string) string {
	return filepath.Join(l.EdgeOutputDir(taskID, edgeID), handoffFileName)
}

// EnsureToolDir creates the directory used to persist one registered tool.
func (l *Layout) EnsureToolDir(name string) error {
	return os.MkdirAll(l.ToolDir(name), 0o755)
}

// EnsureTaskDir creates the root directory for a task.
func (l *Layout) EnsureTaskDir(taskID string) error {
	return os.MkdirAll(l.TaskDir(taskID), 0o755)
}

// EnsureEdgeDir creates the directories needed to execute one edge.
func (l *Layout) EnsureEdgeDir(taskID, edgeID string) error {
	paths := []string{
		l.EdgeDir(taskID, edgeID),
		l.EdgeOutputDir(taskID, edgeID),
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
