package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tsumina/dango/internal/llm/skill"
)

// NewListDir returns a Tool that lists entries in a directory within root.
// Entries are returned one per line; directories are suffixed with "/".
func NewListDir(root string) skill.Tool {
	return skill.NewFuncTool(
		"list_dir",
		"List the entries of a directory within the skill workspace. Directories are suffixed with '/'.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory path relative to the workspace root. Use '.' for the root.",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("list_dir: parse arguments: %w", err)
			}
			if args.Path == "" {
				args.Path = "."
			}
			p, err := skill.ResolveWorkspacePath(root, args.Path)
			if err != nil {
				return "", fmt.Errorf("list_dir: %w", err)
			}
			entries, err := os.ReadDir(p)
			if err != nil {
				return "", fmt.Errorf("list_dir: %w", err)
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				names = append(names, name)
			}
			sort.Strings(names)
			return strings.Join(names, "\n"), nil
		},
	)
}
