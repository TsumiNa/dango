package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// newDeleteFile returns a Tool that removes a file or empty directory within
// the skill workspace.
//
// Relative paths resolve through the temp playground. Absolute paths are
// accepted only inside the temp playground, source workspace, or user-added
// accessible directories, so the tool cannot touch files outside the Skill
// workspace. Directory removal is opt-in via the recursive flag. This tool
// fills the gap left by omitting rm from the default bash allowlist.
func newDeleteFile(ws workspace) tool {
	return newFuncTool(
		"delete_file",
		"Delete a file or directory within the skill temp playground, source workspace, or user-added accessible directories. Relative paths resolve in the temp playground; absolute paths must stay inside one of those roots. Set recursive=true to remove a non-empty directory.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File or directory path. Relative paths resolve inside the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories.",
				},
				"recursive": map[string]any{
					"type":        "boolean",
					"description": "Required to remove a non-empty directory. Default false.",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path      string `json:"path"`
				Recursive bool   `json:"recursive,omitempty"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("delete_file: parse arguments: %w", err)
			}
			p, err := ws.ResolvePath(args.Path)
			if err != nil {
				return "", fmt.Errorf("delete_file: %w", err)
			}
			if args.Recursive {
				if err := os.RemoveAll(p); err != nil {
					return "", fmt.Errorf("delete_file: %w", err)
				}
				return fmt.Sprintf("removed %s (recursive)", args.Path), nil
			}
			if err := os.Remove(p); err != nil {
				return "", fmt.Errorf("delete_file: %w", err)
			}
			return fmt.Sprintf("removed %s", args.Path), nil
		},
	)
}
