package instructions

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed *.md
var instructionFS embed.FS

// StageNote returns the markdown note for an agent stage.
func StageNote(stage string) (string, error) {
	name := strings.TrimSpace(stage)
	if name == "" {
		return "", fmt.Errorf("engine/instructions: stage must not be empty")
	}
	if !validStageName(name) {
		return "", fmt.Errorf("engine/instructions: invalid stage %q", stage)
	}
	raw, err := instructionFS.ReadFile(name + ".md")
	if err != nil {
		return "", fmt.Errorf("engine/instructions: read %s note: %w", name, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func validStageName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}
