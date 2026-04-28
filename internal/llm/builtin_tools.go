package llm

import (
	"fmt"

	"github.com/tsumina/dango/internal/llm/internal/builtin"
)

// BuiltinTools returns the default filesystem and shell tools for s.
//
// The tools use the private temp playground allocated when the Skill is
// created. Relative paths and shell commands run in that temp directory;
// absolute paths are accepted only when they stay inside that temp directory
// or, for [New] skills loaded from a host directory, the source workspace
// root. Directories added with [Skill.WithAccessibleDirs] are also accepted by
// absolute path.
func (s *Skill) BuiltinTools() ([]Tool, error) {
	if s == nil {
		return nil, fmt.Errorf("skill: BuiltinTools requires a non-nil skill")
	}
	if s.workspace == nil || s.workspace.TempRoot() == "" {
		return nil, fmt.Errorf("skill: built-in tools require a temp workspace")
	}
	internalTools := builtin.Tools(s.workspace, s.bashAllow, s.bashBlock)
	tools := make([]Tool, len(internalTools))
	for i, tool := range internalTools {
		tools[i] = tool
	}
	return tools, nil
}

// WithTools returns a fresh lightweight copy of s with tools appended to its
// existing tool set. It must be called before the skill is bound.
func (s *Skill) WithTools(tools ...Tool) (*Skill, error) {
	if s == nil {
		return nil, fmt.Errorf("skill: WithTools requires a non-nil skill")
	}
	if s.conv != nil {
		return nil, fmt.Errorf("skill: WithTools requires an unbound skill")
	}
	combined := append([]Tool(nil), s.tools...)
	combined = append(combined, tools...)
	if err := validateTools(combined); err != nil {
		return nil, err
	}
	bound := s.copy()
	bound.tools = combined
	return bound, nil
}
