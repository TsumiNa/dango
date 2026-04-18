package skill

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/adrg/frontmatter"
	"github.com/tsumina/dango/internal/llm"
)

// SkillFile is the required filename inside a skill directory that carries the
// skill's frontmatter metadata and prompt body.
const SkillFile = "SKILL.md"

// Skill describes a single skill loaded from a skill directory.
//
// A skill directory is laid out as:
//
//	<dir>/
//	    SKILL.md       // required: frontmatter + prompt body
//	    scripts/       // optional
//	    references/    // optional
//	    examples/      // optional
//
// The frontmatter at the top of SKILL.md populates the metadata fields, and
// the remaining body becomes Instruction. Each Skill is bound to a [Client]
// that it uses to invoke the underlying LLM when the skill is executed.
type Skill struct {
	Name        string `yaml:"name" toml:"name" json:"name"`
	Description string `yaml:"description" toml:"description" json:"description"`
	License     string `yaml:"license,omitempty" toml:"license,omitempty" json:"license,omitempty"`
	Instruction string

	client *llm.Client
}

// Client returns the LLM client this skill is bound to.
func (s *Skill) Client() *llm.Client { return s.client }

// NewSkillFromDir loads a Skill from a skill directory rooted at dir and binds
// it to the given LLM client.
//
// dir must point to a directory containing a SKILL.md file. The frontmatter
// in SKILL.md is decoded into the Skill metadata fields and the remaining
// body is stored in Instruction. client must be non-nil and is retained on the
// returned Skill so it can later invoke the LLM. Other entries in the
// directory such as scripts, references, and examples are not read here;
// callers that need them can resolve their own paths relative to dir.
func NewSkillFromDir(dir string, client *llm.Client) (*Skill, error) {
	if client == nil {
		return nil, fmt.Errorf("llm: skill requires a non-nil client")
	}

	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill path %q is not a directory", dir)
	}

	skillPath := filepath.Join(dir, SkillFile)
	file, err := os.Open(skillPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("skill directory %q is missing required %s: %w", dir, SkillFile, err)
		}
		return nil, fmt.Errorf("open %s in %q: %w", SkillFile, dir, err)
	}
	defer file.Close()

	var skill Skill
	rest, err := frontmatter.Parse(file, &skill)
	if err != nil {
		return nil, err
	}
	skill.Instruction = string(rest)
	skill.client = client
	return &skill, nil
}
