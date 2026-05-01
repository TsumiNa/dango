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
// or, for [NewSkill] skills loaded from a host directory, the source workspace
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

// WithAccessibleDirsAndBuiltinTools returns a fresh lightweight copy of s with
// built-in filesystem tools rebuilt against the supplied accessible
// directories.
//
// Existing non-built-in tools are preserved. Existing built-in tools are
// replaced so their workspace resolver sees the new directory set. It must be
// called before the skill is bound.
func (s *Skill) WithAccessibleDirsAndBuiltinTools(dirs ...string) (*Skill, error) {
	if s == nil {
		return nil, fmt.Errorf("skill: WithAccessibleDirsAndBuiltinTools requires a non-nil skill")
	}
	if s.conv != nil {
		return nil, fmt.Errorf("skill: WithAccessibleDirsAndBuiltinTools requires an unbound skill")
	}
	copySkill := s.copy()
	customTools := copySkill.tools[:0]
	for _, tool := range copySkill.tools {
		if tool == nil || isBuiltinToolName(tool.Name()) {
			continue
		}
		customTools = append(customTools, tool)
	}
	copySkill.tools = append([]Tool(nil), customTools...)
	if err := copySkill.WithAccessibleDirs(dirs...); err != nil {
		return nil, err
	}
	builtinTools, err := copySkill.BuiltinTools()
	if err != nil {
		return nil, err
	}
	copySkill.tools = append(copySkill.tools, builtinTools...)
	if err := validateTools(copySkill.tools); err != nil {
		return nil, err
	}
	return copySkill, nil
}

func isBuiltinToolName(name string) bool {
	switch name {
	case "bash", "read_file", "write_file", "edit_file", "delete_file", "move_file", "list_dir", "grep", "pwd":
		return true
	default:
		return false
	}
}
