package executor

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/tsumina/dango/internal/spec"
)

func writeAutoHandoff(outputPath, toolName, taskID, summary string) error {
	files, err := collectOutputFiles(outputPath)
	if err != nil {
		return err
	}

	return writeHandoff(outputPath, spec.Handoff{
		Metadata: spec.HandoffMetadata{
			TaskID:      taskID,
			Tool:        toolName,
			Status:      spec.HandoffStatusCompleted,
			OutputFiles: files,
			Timestamp:   time.Now().UTC(),
		},
		Body: "## Description\n\n" + summary,
	})
}

func writeFailureHandoff(outputPath, toolName, taskID string, executionErr error) error {
	return writeHandoff(outputPath, spec.Handoff{
		Metadata: spec.HandoffMetadata{
			TaskID:    taskID,
			Tool:      toolName,
			Status:    spec.HandoffStatusFailed,
			Timestamp: time.Now().UTC(),
			Error:     executionErr.Error(),
		},
		Body: "## Description\n\nTool execution failed before producing a handoff.",
	})
}

func writeHandoff(outputPath string, handoff spec.Handoff) error {
	payload, err := spec.RenderHandoff(handoff)
	if err != nil {
		return err
	}

	path := filepath.Join(outputPath, "_handoff.md")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write handoff %q: %w", path, err)
	}
	return nil
}

func collectOutputFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "_handoff.md" {
			return nil
		}

		out = append(out, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk output directory %q: %w", root, err)
	}

	sort.Strings(out)
	return out, nil
}
