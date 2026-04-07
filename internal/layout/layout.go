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

type Layout struct {
	Root string
}

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

func (l *Layout) Ensure() error {
	for _, dir := range []string{l.Root, l.RegistryDir(), l.TasksDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %q: %w", dir, err)
		}
	}

	return nil
}

func (l *Layout) DBPath() string {
	return filepath.Join(l.Root, dbFileName)
}

func (l *Layout) RegistryDir() string {
	return filepath.Join(l.Root, registryDirName)
}

func (l *Layout) TasksDir() string {
	return filepath.Join(l.Root, tasksDirName)
}

func (l *Layout) ToolDir(name string) string {
	return filepath.Join(l.RegistryDir(), safeComponent(name))
}

func (l *Layout) ToolSpecPath(name string) string {
	return filepath.Join(l.ToolDir(name), "tool.yaml")
}

func (l *Layout) ToolOverridePath(name string) string {
	return filepath.Join(l.ToolDir(name), "override.yaml")
}

func (l *Layout) ToolMergedPath(name string) string {
	return filepath.Join(l.ToolDir(name), "merged.yaml")
}

func (l *Layout) TaskDir(taskID string) string {
	return filepath.Join(l.TasksDir(), taskID)
}

func (l *Layout) TaskRequestPath(taskID string) string {
	return filepath.Join(l.TaskDir(taskID), "task.md")
}

func (l *Layout) TaskResultPath(taskID string) string {
	return filepath.Join(l.TaskDir(taskID), "result.md")
}

func (l *Layout) EdgeDir(taskID, edgeID string) string {
	return filepath.Join(l.TaskDir(taskID), "edges", edgeID)
}

func (l *Layout) EdgeSubTaskPath(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), "sub-task.md")
}

func (l *Layout) EdgeOutputDir(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), "output")
}

func (l *Layout) EdgeScratchInputDir(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), ".input-empty")
}

func (l *Layout) EdgeHandoffPath(taskID, edgeID string) string {
	return filepath.Join(l.EdgeOutputDir(taskID, edgeID), handoffFileName)
}

func (l *Layout) EnsureToolDir(name string) error {
	return os.MkdirAll(l.ToolDir(name), 0o755)
}

func (l *Layout) EnsureTaskDir(taskID string) error {
	return os.MkdirAll(l.TaskDir(taskID), 0o755)
}

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
