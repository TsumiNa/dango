package runner

import (
	"os"
	"path/filepath"
)

const (
	// ExchangeResourceFile marks a front matter resource path as a file.
	ExchangeResourceFile = "file"
	// ExchangeResourceDir marks a front matter resource path as a directory.
	ExchangeResourceDir = "dir"
)

func exchangeResourceDirsFromOutputs(outputs map[string]any) []string {
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
			if dir, ok := exchangeResourceDir(resource); ok && !containsDir(dirs, dir) {
				dirs = append(dirs, dir)
			}
		}
	}
	return dirs
}

func exchangeResourceDir(resource ExchangeResource) (string, bool) {
	if resource.Path == "" || !filepath.IsAbs(resource.Path) {
		return "", false
	}
	path := resource.Path
	switch resource.Type {
	case ExchangeResourceDir:
		return canonicalExistingDir(path)
	case ExchangeResourceFile:
		return canonicalExistingDir(filepath.Dir(path))
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		return canonicalExistingDir(path)
	}
	return canonicalExistingDir(filepath.Dir(path))
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
