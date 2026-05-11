package instructions

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed *.md
var instructionFS embed.FS

// StageNote returns the markdown note for an executor stage.
func StageNote(stage string) (string, error) {
	name := strings.TrimSpace(stage)
	if name == "" {
		return "", fmt.Errorf("engine/builtin/instructions: stage must not be empty")
	}
	raw, err := instructionFS.ReadFile(name + ".md")
	if err != nil {
		return "", fmt.Errorf("engine/builtin/instructions: read %s note: %w", name, err)
	}
	return strings.TrimSpace(string(raw)), nil
}
