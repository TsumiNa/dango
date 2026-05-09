package runner

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// HandoffArtifactFile marks a handoff artifact path as a file.
	HandoffArtifactFile = "file"
	// HandoffArtifactDir marks a handoff artifact path as a directory.
	HandoffArtifactDir = "dir"
)

func handoffArtifactDirsFromOutputs(outputs map[string]any, allowedRoots []string) []string {
	if len(outputs) == 0 {
		return nil
	}
	var dirs []string
	for _, output := range outputs {
		text, ok := output.(string)
		if !ok {
			continue
		}
		doc, err := ParseHandoffMarkdown(text)
		if err != nil {
			continue
		}
		for _, artifact := range doc.Artifacts {
			if dir, ok := handoffArtifactDir(artifact, allowedRoots); ok && !containsDir(dirs, dir) {
				dirs = append(dirs, dir)
			}
		}
	}
	return dirs
}

func handoffArtifactDir(artifact HandoffArtifact, allowedRoots []string) (string, bool) {
	if artifact.Path == "" || !filepath.IsAbs(artifact.Path) {
		return "", false
	}
	path := artifact.Path
	var dir string
	switch artifact.Type {
	case HandoffArtifactDir:
		resolved, ok := canonicalExistingDir(path)
		if !ok {
			return "", false
		}
		dir = resolved
	case HandoffArtifactFile:
		resolved, ok := canonicalExistingDir(filepath.Dir(path))
		if !ok {
			return "", false
		}
		dir = resolved
	default:
		info, err := os.Stat(path)
		if err != nil {
			return "", false
		}
		target := path
		if !info.IsDir() {
			target = filepath.Dir(path)
		}
		resolved, ok := canonicalExistingDir(target)
		if !ok {
			return "", false
		}
		dir = resolved
	}
	if !dirUnderAllowedRoots(dir, allowedRoots) {
		return "", false
	}
	return dir, true
}

func dirUnderAllowedRoots(dir string, allowedRoots []string) bool {
	if len(allowedRoots) == 0 {
		return true
	}
	for _, root := range allowedRoots {
		if root == "" {
			continue
		}
		rootPrefix := filepath.Clean(root) + string(filepath.Separator)
		cleaned := filepath.Clean(dir) + string(filepath.Separator)
		if cleaned == rootPrefix || strings.HasPrefix(cleaned, rootPrefix) {
			return true
		}
	}
	return false
}

func canonicalExistingDir(dir string) (string, bool) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return real, true
}

func containsDir(dirs []string, dir string) bool {
	for _, existing := range dirs {
		if existing == dir {
			return true
		}
	}
	return false
}
