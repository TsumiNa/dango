package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// newListDir returns a Tool that lists entries in a directory within the temp
// playground, source workspace, or a user-added accessible directory.
// Entries are returned one per line; directories are suffixed with "/".
func newListDir(ws workspace) tool {
	return newFuncTool(
		"list_dir",
		"List entries in a directory within the skill temp playground, source workspace, or user-added accessible directories. Relative paths resolve in the temp playground; absolute paths must stay inside one of those roots. Directories are suffixed with '/'.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory path. Relative paths resolve inside the temp playground; use '.' for the temp root. Absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories.",
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
			p, err := ws.ResolvePath(args.Path)
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
