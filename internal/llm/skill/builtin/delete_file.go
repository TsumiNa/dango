package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/tsumina/dango/internal/llm/skill"
)

// NewDeleteFile returns a Tool that removes a file or empty directory within
// the skill workspace.
//
// Paths resolve against root and absolute paths or escapes are rejected, so
// the tool cannot touch files outside the workspace. Directory removal is
// opt-in via the recursive flag and still confined to root. This tool fills
// the gap left by omitting rm from the default bash allowlist.
func NewDeleteFile(root string) skill.Tool {
	return skill.NewFuncTool(
		"delete_file",
		"Delete a file or directory within the skill workspace. Set recursive=true to remove a non-empty directory. Paths escaping the workspace root are rejected.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file or directory relative to the workspace root.",
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
			p, err := skill.ResolveWorkspacePath(root, args.Path)
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
