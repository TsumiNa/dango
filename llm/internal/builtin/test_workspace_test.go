package builtin

import (
	"fmt"
	"path/filepath"
	"strings"
)

type testWorkspace struct {
	root string
}

func (w testWorkspace) Root() string { return w.root }

func (w testWorkspace) WorkDir() string { return w.root }

func (w testWorkspace) SkillRoot() string { return w.root }

func (w testWorkspace) TempRoot() string { return w.root }

func (w testWorkspace) AccessibleDirs() []string { return nil }

func (w testWorkspace) ResolvePath(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	absRoot, err := filepath.Abs(w.root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	cleaned := filepath.Clean(rel)
	if !filepath.IsAbs(cleaned) {
		cleaned = filepath.Clean(filepath.Join(absRoot, rel))
	}
	relCheck, err := filepath.Rel(absRoot, cleaned)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace root", rel)
	}
	return cleaned, nil
}
