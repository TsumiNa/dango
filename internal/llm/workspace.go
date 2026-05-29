package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type workspaceRoot struct {
	skillRoot  string
	tempRoot   string
	extraRoots []string
}

func (w *workspaceRoot) copy() *workspaceRoot {
	if w == nil {
		return nil
	}
	clone := *w
	clone.extraRoots = append([]string(nil), w.extraRoots...)
	return &clone
}

func newWorkspaceRoot(root string) (*workspaceRoot, error) {
	skillRoot, err := canonicalDir(root)
	if err != nil {
		return nil, err
	}
	workspace, err := newTempWorkspaceRoot()
	if err != nil {
		return nil, err
	}
	workspace.skillRoot = skillRoot
	return workspace, nil
}

func newTempWorkspaceRoot() (*workspaceRoot, error) {
	tempRoot, err := os.MkdirTemp("", "dango-skill-*")
	if err != nil {
		return nil, fmt.Errorf("skill: create temp workspace: %w", err)
	}
	realTempRoot, err := canonicalDir(tempRoot)
	if err != nil {
		_ = os.RemoveAll(tempRoot)
		return nil, err
	}
	return &workspaceRoot{tempRoot: realTempRoot}, nil
}

func canonicalDir(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("skill: workspace root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("skill: resolve workspace root: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("skill: resolve workspace root symlinks: %w", err)
	}
	info, err := os.Stat(realRoot)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("skill path %q is not a directory", root)
	}
	return realRoot, nil
}

func (w *workspaceRoot) WorkDir() string {
	if w == nil {
		return ""
	}
	return w.tempRoot
}

func (w *workspaceRoot) SkillRoot() string {
	if w == nil {
		return ""
	}
	return w.skillRoot
}

func (w *workspaceRoot) TempRoot() string {
	if w == nil {
		return ""
	}
	return w.tempRoot
}

func (w *workspaceRoot) AccessibleDirs() []string {
	if w == nil {
		return nil
	}
	return append([]string(nil), w.extraRoots...)
}

func (w *workspaceRoot) setAccessibleDirs(dirs ...string) error {
	if w == nil {
		return fmt.Errorf("workspace root is not configured")
	}
	roots := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		root, err := canonicalDir(dir)
		if err != nil {
			return err
		}
		if !containsPath(roots, root) {
			roots = append(roots, root)
		}
	}
	w.extraRoots = roots
	return nil
}

func (w *workspaceRoot) cleanup() error {
	if w == nil || w.tempRoot == "" {
		return nil
	}
	return os.RemoveAll(w.tempRoot)
}

func (w *workspaceRoot) ResolvePath(rel string) (string, error) {
	if w == nil {
		return "", fmt.Errorf("workspace root is not configured")
	}
	if filepath.IsAbs(rel) {
		return w.resolveAbsolutePath(rel)
	}
	return resolveWorkspacePath(w.tempRoot, rel)
}

func (w *workspaceRoot) resolveAbsolutePath(p string) (string, error) {
	cleaned := filepath.Clean(p)
	resolved, err := resolvePathExistingPrefix(cleaned)
	if err != nil {
		return "", err
	}
	for _, root := range w.roots() {
		if root == "" || !pathWithinRoot(root, resolved) {
			continue
		}
		return resolved, nil
	}
	return "", fmt.Errorf("path %q escapes workspace root", p)
}

func (w *workspaceRoot) roots() []string {
	roots := make([]string, 0, 2+len(w.extraRoots))
	if w.tempRoot != "" {
		roots = append(roots, w.tempRoot)
	}
	if w.skillRoot != "" {
		roots = append(roots, w.skillRoot)
	}
	roots = append(roots, w.extraRoots...)
	return roots
}

func resolveWorkspacePath(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the workspace root", rel)
	}
	cleanRel := filepath.Clean(rel)
	if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace root", rel)
	}

	cleaned := filepath.Clean(filepath.Join(root, cleanRel))
	resolved, err := resolveExistingPrefix(root, cleaned)
	if err != nil {
		return "", err
	}
	if !pathWithinRoot(root, resolved) {
		return "", fmt.Errorf("path %q escapes workspace root", rel)
	}
	return resolved, nil
}

func resolveExistingPrefix(root, target string) (string, error) {
	resolved, err := resolvePathExistingPrefix(target)
	if err != nil {
		return "", err
	}
	if !pathWithinRoot(root, resolved) {
		return "", fmt.Errorf("path %q escapes workspace root", target)
	}
	return resolved, nil
}

func resolvePathExistingPrefix(target string) (string, error) {
	current := target
	var missing []string
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("path %q cannot be resolved", target)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func pathWithinRoot(root, target string) bool {
	relCheck, err := filepath.Rel(root, target)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func containsPath(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}
