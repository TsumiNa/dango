package skill

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResolveWorkspacePath resolves rel against root and ensures the cleaned
// result stays inside root. It returns a cleaned absolute path on success
// and is the standard helper that built-in filesystem tools (and
// third-party tools that want the same containment guarantees) should use
// to validate user-supplied paths.
//
// rel must be non-empty and relative; absolute paths and parent traversals
// that escape root are rejected.
func ResolveWorkspacePath(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the workspace root", rel)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	cleaned := filepath.Clean(filepath.Join(absRoot, rel))
	relCheck, err := filepath.Rel(absRoot, cleaned)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace root", rel)
	}
	return cleaned, nil
}
