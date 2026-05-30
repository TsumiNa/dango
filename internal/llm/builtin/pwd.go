package builtin

import (
	"context"
	"fmt"
	"strings"
)

// newPwd returns a Tool that reports the skill workspace, temp playground, and
// user-added accessible directories.
func newPwd(ws workspace) tool {
	return newFuncTool(
		"pwd",
		"Return the absolute paths of the skill source workspace, private temp playground, and user-added accessible directories.",
		map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		func(ctx context.Context, arguments string) (string, error) {
			var out strings.Builder
			if root := ws.SkillRoot(); root != "" {
				fmt.Fprintf(&out, "skill_root: %s\n", root)
			}
			fmt.Fprintf(&out, "temp_root: %s\n", ws.TempRoot())
			fmt.Fprintf(&out, "workdir: %s", ws.WorkDir())
			for _, dir := range ws.AccessibleDirs() {
				fmt.Fprintf(&out, "\naccessible_dir: %s", dir)
			}
			return out.String(), nil
		},
	)
}
