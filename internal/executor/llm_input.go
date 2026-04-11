package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxInputContextFiles = 8
	maxInputPreviewBytes = 4096
)

type inputContextSummary struct {
	InputPath string                 `json:"input_path,omitempty"`
	InputURL  string                 `json:"input_url,omitempty"`
	Files     []inputContextFile     `json:"files,omitempty"`
	Meta      map[string]interface{} `json:"meta,omitempty"`
}

type inputContextFile struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	TextPreview string `json:"text_preview,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

func summarizeInputContext(runtimeContext runtimeContext) (string, error) {
	summary := inputContextSummary{
		InputPath: strings.TrimSpace(runtimeContext.InputPath),
		InputURL:  strings.TrimSpace(runtimeContext.InputURL),
		Meta:      map[string]interface{}{},
	}

	if summary.InputPath != "" {
		files, truncated, err := collectInputContextFiles(summary.InputPath)
		if err != nil {
			return "", err
		}
		summary.Files = files
		if truncated {
			summary.Meta["files_truncated"] = true
		}
	}
	if len(summary.Meta) == 0 {
		summary.Meta = nil
	}

	payload, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal input context summary: %w", err)
	}
	return string(payload), nil
}

func collectInputContextFiles(root string) ([]inputContextFile, bool, error) {
	files := make([]inputContextFile, 0, maxInputContextFiles)
	truncated := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if len(files) >= maxInputContextFiles {
			truncated = true
			return filepath.SkipAll
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := inputContextFile{
			Path: filepath.ToSlash(relative),
			Size: info.Size(),
		}

		preview, previewTruncated, err := readTextPreview(path)
		if err != nil {
			return err
		}
		item.TextPreview = preview
		item.Truncated = previewTruncated
		files = append(files, item)
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("walk input context %q: %w", root, err)
	}
	return files, truncated, nil
}

func readTextPreview(path string) (string, bool, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	if !looksLikeText(payload) {
		return "", false, nil
	}
	if len(payload) <= maxInputPreviewBytes {
		return strings.TrimSpace(string(payload)), false, nil
	}
	return strings.TrimSpace(string(payload[:maxInputPreviewBytes])), true, nil
}

func looksLikeText(payload []byte) bool {
	if len(payload) == 0 {
		return true
	}
	if bytes.IndexByte(payload, 0) >= 0 {
		return false
	}
	return utf8.Valid(payload)
}
