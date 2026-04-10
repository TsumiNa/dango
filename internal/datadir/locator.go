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

// Locator resolves the canonical on-disk paths used by a dango data
// directory.
//
// The zero value is not usable. Construct [Locator] values with [New] so Root
// is normalized to an absolute path. Registry persistence, task persistence,
// runner scheduling, and tests all depend on Locator to keep the filesystem
// layout stable.
type Locator struct {
	// Root is the absolute path to the orchestrator data directory.
	Root string
}

// New validates root and returns a [Locator] rooted at its absolute filesystem
// path.
//
// New normalizes the path but does not create any directories. Callers should
// use [Locator.Ensure] before writing top-level state.
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
//
// Ensure does not create per-tool or per-task directories; those are created by
// the more specific Ensure* helpers as workflow state is materialized.
func (l *Locator) Ensure() error {
	for _, dir := range []string{l.Root, l.RegistryDir(), l.TasksDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %q: %w", dir, err)
		}
	}

	return nil
}

// DBPath returns the SQLite database path for the data directory root.
//
// SQLite store initialization uses this path as the single durable database for
// registry, task, edge, and log rows.
func (l *Locator) DBPath() string {
	return filepath.Join(l.Root, dbFileName)
}

// RegistryDir returns the directory containing persisted registry data for all
// tools.
//
// Each registered tool receives a stable subdirectory beneath this root.
func (l *Locator) RegistryDir() string {
	return filepath.Join(l.Root, registryDirName)
}

// TasksDir returns the directory containing all per-task execution data.
//
// Each task receives its own directory tree for request, metadata, plan-adjacent
// artifacts, edge workspaces, and final results.
func (l *Locator) TasksDir() string {
	return filepath.Join(l.Root, tasksDirName)
}

// ToolDir returns the stable registry directory used to store one registered
// tool.
//
// The final path component is sanitized so tool names cannot escape the
// registry root.
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

// TaskDir returns the root artifact directory for one task.
//
// Orchestrator services, the runner, and tests all use this as the stable root
// for task-local files.
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

// EdgeDir returns the working directory for one planned edge within a task.
//
// The scheduler and executor contract use this directory as the parent for the
// sub-task markdown, public output, private output, and scratch input paths.
func (l *Locator) EdgeDir(taskID, edgeID string) string {
	return filepath.Join(l.TaskDir(taskID), "edges", edgeID)
}

// EdgeSubTaskPath returns the sub-task markdown path for an edge.
func (l *Locator) EdgeSubTaskPath(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), "sub-task.md")
}

// EdgeOutputDir returns the output directory for an edge.
//
// This is the public artifact directory whose contents may be surfaced to the
// orchestrator and user-facing task views.
func (l *Locator) EdgeOutputDir(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), "output")
}

// EdgePrivateOutputDir returns the private downstream-only output directory for
// an edge.
//
// Executors place _handoff.md and downstream-only machine artifacts here so
// later edges can consume them without exposing everything as public output.
func (l *Locator) EdgePrivateOutputDir(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), "_output")
}

// EdgeScratchInputDir returns the empty scratch input directory for root edges.
//
// The scheduler uses this when an edge has no upstream dependencies but the
// executor contract still requires an input directory.
func (l *Locator) EdgeScratchInputDir(taskID, edgeID string) string {
	return filepath.Join(l.EdgeDir(taskID, edgeID), ".input-empty")
}

// EdgeMergedInputDir returns the directory used to merge multiple dependency
// inputs.
//
// The scheduler populates this directory with per-dependency links when an edge
// depends on more than one upstream output.
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
//
// Callers typically invoke this before writing tool.yaml, override.yaml, or
// merged.yaml.
func (l *Locator) EnsureToolDir(name string) error {
	return os.MkdirAll(l.ToolDir(name), 0o755)
}

// EnsureTaskDir creates the root artifact directory for a task.
func (l *Locator) EnsureTaskDir(taskID string) error {
	return os.MkdirAll(l.TaskDir(taskID), 0o755)
}

// EnsureEdgeDir creates the directory tree needed to execute one edge.
//
// In particular it creates the edge root plus the public and private output
// directories expected by the scheduler and executor runtime contract.
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
