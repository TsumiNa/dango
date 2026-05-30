package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// newEditFile returns a Tool that performs a targeted string replacement
// within a single file in the temp playground, source workspace, or a
// user-added accessible directory.
//
// The tool requires old_string to appear exactly once in the file; callers
// should include enough surrounding context to make the match unique. This
// lets the model amend large files without having to rewrite them via
// [newWriteFile], saving tokens and avoiding accidental deletions.
func newEditFile(ws workspace) tool {
	return newFuncTool(
		"edit_file",
		"Replace a unique occurrence of old_string with new_string inside an existing file in the temp playground, source workspace, or user-added accessible directories. Relative paths resolve in the temp playground; absolute paths must stay inside one of those roots. old_string must match exactly once; include enough surrounding context to disambiguate.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path. Relative paths resolve inside the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories.",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "Exact substring to replace. Must appear exactly once in the file.",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "Replacement text. Use an empty string to delete old_string.",
				},
			},
			"required":             []string{"path", "old_string", "new_string"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path      string `json:"path"`
				OldString string `json:"old_string"`
				NewString string `json:"new_string"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("edit_file: parse arguments: %w", err)
			}
			if args.OldString == "" {
				return "", fmt.Errorf("edit_file: old_string must not be empty")
			}
			p, err := ws.ResolvePath(args.Path)
			if err != nil {
				return "", fmt.Errorf("edit_file: %w", err)
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return "", fmt.Errorf("edit_file: %w", err)
			}
			count := strings.Count(string(data), args.OldString)
			switch {
			case count == 0:
				return "", fmt.Errorf("edit_file: old_string not found in %s", args.Path)
			case count > 1:
				return "", fmt.Errorf("edit_file: old_string is not unique in %s (found %d matches)", args.Path, count)
			}
			updated := strings.Replace(string(data), args.OldString, args.NewString, 1)
			if err := os.WriteFile(p, []byte(updated), 0o644); err != nil {
				return "", fmt.Errorf("edit_file: %w", err)
			}
			return fmt.Sprintf("replaced 1 occurrence in %s", args.Path), nil
		},
	)
}
