package runner

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// ExchangeResourceFile marks a front matter resource path as a file.
	ExchangeResourceFile = "file"
	// ExchangeResourceDir marks a front matter resource path as a directory.
	ExchangeResourceDir = "dir"
)

// exchangeResourceDirsFromOutputs collects the containing directories of all
// resources declared in upstream exchange documents. allowedRoots constrains
// which directories may be granted: a resolved directory is included only when
// it is rooted under at least one entry in allowedRoots. An empty allowedRoots
// disables the constraint (no filtering).
func exchangeResourceDirsFromOutputs(outputs map[string]any, allowedRoots []string) []string {
	if len(outputs) == 0 {
		return nil
	}
	var dirs []string
	for _, output := range outputs {
		text, ok := output.(string)
		if !ok {
			continue
		}
		doc, err := ParseExchangeMarkdown(text)
		if err != nil {
			continue
		}
		for _, resource := range doc.Resources {
			if dir, ok := exchangeResourceDir(resource, allowedRoots); ok && !containsDir(dirs, dir) {
				dirs = append(dirs, dir)
			}
		}
	}
	return dirs
}

func exchangeResourceDir(resource ExchangeResource, allowedRoots []string) (string, bool) {
	if resource.Path == "" || !filepath.IsAbs(resource.Path) {
		return "", false
	}
	path := resource.Path
	var dir string
	switch resource.Type {
	case ExchangeResourceDir:
		resolved, ok := canonicalExistingDir(path)
		if !ok {
			return "", false
		}
		dir = resolved
	case ExchangeResourceFile:
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

// dirUnderAllowedRoots reports whether dir is rooted under at least one entry
// in allowedRoots. When allowedRoots is empty the check is skipped (permit
// all), so callers that have no configured roots keep the previous behaviour.
func dirUnderAllowedRoots(dir string, allowedRoots []string) bool {
	if len(allowedRoots) == 0 {
		return true
	}
	for _, root := range allowedRoots {
		if root == "" {
			continue
		}
		// Ensure root ends with separator so "/foobar" is not matched by root "/foo".
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
