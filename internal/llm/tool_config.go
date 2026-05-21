package llm

import "github.com/tsumina/dango/internal/llm/internal/builtin"

// ExtraTool names an opt-in built-in tool that is appended after the default
// built-in tool set.
type ExtraTool = builtin.ExtraTool

const (
	// ExtraListDir enables the list_dir tool.
	ExtraListDir = builtin.ExtraListDir
	// ExtraPwd enables the pwd tool.
	ExtraPwd = builtin.ExtraPwd
)

// ParseExtraTool converts a config string into a typed extra tool value.
func ParseExtraTool(name string) (ExtraTool, error) { return builtin.ParseExtraTool(name) }

// ToolSetConfig is the single typed input that controls which built-in tools
// are enabled for a skill.
type ToolSetConfig = builtin.ToolSetConfig

// DefaultToolSetConfig returns the default built-in tool availability for a
// newly loaded skill.
func DefaultToolSetConfig() ToolSetConfig { return ToolSetConfig{} }
