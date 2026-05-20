package builtin

import "fmt"

// ExtraTool names an opt-in built-in tool that is not part of the core floor.
type ExtraTool string

const (
	// ExtraListDir enables the list_dir tool.
	ExtraListDir ExtraTool = "list_dir"
	// ExtraPwd enables the pwd tool.
	ExtraPwd ExtraTool = "pwd"
)

// String returns the tool name exposed to the model.
func (t ExtraTool) String() string { return string(t) }

// ParseExtraTool converts a config string into a typed extra tool value.
func ParseExtraTool(name string) (ExtraTool, error) {
	switch ExtraTool(name) {
	case ExtraListDir:
		return ExtraListDir, nil
	case ExtraPwd:
		return ExtraPwd, nil
	default:
		return "", fmt.Errorf("builtin: unknown extra tool %q", name)
	}
}

// ToolSetConfig describes which opt-in builtin capabilities are added on top
// of the always-available core tool floor.
type ToolSetConfig struct {
	BashAllow []string
	BashBlock []string
	Extras    []ExtraTool
}
