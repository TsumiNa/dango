package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	persistencepkg "github.com/tsumina/dango/internal/engine/runner/persistence"
)

// Workspace holds the runner-owned workspace directory layout.
type Workspace struct {
	globalRoot string
	root       string
	exchange   string
	archive    string
	skills     map[string]SkillWorkspace
}

// SkillWorkspace holds one skill's workspace directories.
type SkillWorkspace struct {
	NodeID         string
	Root           string
	MemoDir        string
	InboxDir       string
	OutboxDir      string
	WorkingDir     string
	accessibleDirs []string
}

// ProvisionWorkspace creates the runner workspace tree and computes
// per-skill accessible directories.
func ProvisionWorkspace(globalRoot string, runnerID string, nodeIDs []string, rule persistencepkg.PathRule) (*Workspace, error) {
	root, err := ensureCanonicalDir(globalRoot)
	if err != nil {
		return nil, fmt.Errorf("runner: resolve workspace root: %w", err)
	}
	if runnerID == "" {
		return nil, fmt.Errorf("runner: runner id is required")
	}
	if rule == nil {
		rule = persistencepkg.DefaultPathRule
	}
	subdir, err := validateRulePath(rule(runnerID))
	if err != nil {
		return nil, err
	}
	runnerRoot := filepath.Join(root, subdir)
	if !pathWithinRoot(root, runnerRoot) {
		return nil, fmt.Errorf("runner: path rule output %q escapes workspace root", subdir)
	}
	if _, statErr := os.Stat(runnerRoot); statErr == nil {
		return nil, fmt.Errorf("runner: workspace path collision: %s", runnerRoot)
	} else if !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("runner: stat workspace root: %w", statErr)
	}

	exchange := filepath.Join(runnerRoot, "exchange")
	skillsRoot := filepath.Join(runnerRoot, "skills")
	archive := filepath.Join(runnerRoot, "archive")
	if err := os.MkdirAll(exchange, 0o755); err != nil {
		return nil, fmt.Errorf("runner: create exchange dir: %w", err)
	}
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		return nil, fmt.Errorf("runner: create skills dir: %w", err)
	}
	if err := os.MkdirAll(archive, 0o755); err != nil {
		return nil, fmt.Errorf("runner: create archive dir: %w", err)
	}

	workspace := &Workspace{
		globalRoot: root,
		root:       runnerRoot,
		exchange:   exchange,
		archive:    archive,
		skills:     make(map[string]SkillWorkspace, len(nodeIDs)),
	}
	for _, nodeID := range nodeIDs {
		if err := validateNodeID(nodeID); err != nil {
			return nil, err
		}
		skillRoot := filepath.Join(skillsRoot, nodeID)
		memo := filepath.Join(skillRoot, "memo")
		inbox := filepath.Join(skillRoot, "inbox")
		outbox := filepath.Join(skillRoot, "outbox")
		work := filepath.Join(skillRoot, "workspace")
		for _, dir := range []string{memo, inbox, outbox, work} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("runner: create skill workspace dir: %w", err)
			}
		}
		workspace.skills[nodeID] = SkillWorkspace{
			NodeID:         nodeID,
			Root:           skillRoot,
			MemoDir:        memo,
			InboxDir:       inbox,
			OutboxDir:      outbox,
			WorkingDir:     work,
			accessibleDirs: []string{memo, inbox, outbox, work, exchange},
		}
	}
	return workspace, nil
}

// Root returns the runner workspace root.
func (w *Workspace) Root() string {
	if w == nil {
		return ""
	}
	return w.root
}

// ExchangeDir returns the runner-owned public exchange directory.
func (w *Workspace) ExchangeDir() string {
	if w == nil {
		return ""
	}
	return w.exchange
}

// ArchiveDir returns the runner-owned archive directory.
func (w *Workspace) ArchiveDir() string {
	if w == nil {
		return ""
	}
	return w.archive
}

// Skill returns the workspace directories for one skill node.
func (w *Workspace) Skill(nodeID string) (SkillWorkspace, bool) {
	if w == nil {
		return SkillWorkspace{}, false
	}
	sk, ok := w.skills[nodeID]
	return sk, ok
}

// AccessibleDirs returns the precomputed allowed filesystem roots for a skill.
func (w *Workspace) AccessibleDirs(nodeID string) ([]string, error) {
	sk, ok := w.Skill(nodeID)
	if !ok {
		return nil, fmt.Errorf("runner: unknown workspace skill %q", nodeID)
	}
	return append([]string(nil), sk.accessibleDirs...), nil
}

// Handoff symlinks a producer's outbox handoff and artifacts into a successor
// skill's inbox directory.
//
// Successor skills should treat inbox handoff/artifacts as read-only by policy.
func (w *Workspace) Handoff(producerID string, successorID string) error {
	producer, ok := w.Skill(producerID)
	if !ok {
		return fmt.Errorf("runner: unknown producer skill %q", producerID)
	}
	successor, ok := w.Skill(successorID)
	if !ok {
		return fmt.Errorf("runner: unknown successor skill %q", successorID)
	}
	dst := filepath.Join(successor.InboxDir, producerID)
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("runner: reset inbox route dir: %w", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("runner: create inbox route dir: %w", err)
	}
	if err := symlinkFileIfExists(filepath.Join(producer.OutboxDir, "handoff.md"), filepath.Join(dst, "handoff.md")); err != nil {
		return err
	}
	return symlinkTreeIfExists(filepath.Join(producer.OutboxDir, "artifacts"), filepath.Join(dst, "artifacts"))
}

func validateRulePath(subdir string) (string, error) {
	if subdir == "" {
		return "", fmt.Errorf("runner: path rule returned empty path")
	}
	if filepath.IsAbs(subdir) {
		return "", fmt.Errorf("runner: path rule returned absolute path %q", subdir)
	}
	clean := filepath.Clean(subdir)
	if clean == "." {
		return "", fmt.Errorf("runner: path rule returned empty path")
	}
	if strings.Contains(clean, string(filepath.Separator)) {
		return "", fmt.Errorf("runner: path rule must return a single path element %q", subdir)
	}
	return clean, nil
}

func validateNodeID(nodeID string) error {
	if nodeID == "" {
		return fmt.Errorf("runner: node id is required")
	}
	if strings.Contains(nodeID, string(filepath.Separator)) || nodeID == "." || nodeID == ".." {
		return fmt.Errorf("runner: invalid node id %q", nodeID)
	}
	return nil
}

func ensureCanonicalDir(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("runner: workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("runner: workspace root is not a directory")
	}
	return real, nil
}

func pathWithinRoot(root string, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func symlinkFileIfExists(src string, dst string) error {
	info, err := os.Lstat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("runner: stat source file %q: %w", src, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runner: source file %q must not be a symbolic link", src)
	}
	if info.IsDir() {
		return fmt.Errorf("runner: source %q is a directory", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("runner: create destination parent: %w", err)
	}
	if err := os.Symlink(src, dst); err != nil {
		return fmt.Errorf("runner: symlink file %q -> %q: %w", src, dst, err)
	}
	return nil
}

func symlinkTreeIfExists(src string, dst string) error {
	info, err := os.Lstat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("runner: stat source dir %q: %w", src, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runner: source dir %q must not be a symbolic link", src)
	}
	if !info.IsDir() {
		return fmt.Errorf("runner: source %q is not a directory", src)
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entryInfo, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("runner: source path %q must not be a symbolic link", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return symlinkFileIfExists(path, target)
	})
}
