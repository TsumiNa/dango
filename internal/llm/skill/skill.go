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

	dir       string
	client    *llm.Client
	bashAllow []string
	bashBlock []string
}

// Client returns the LLM client this skill is bound to.
func (s *Skill) Client() *llm.Client { return s.client }

// Dir returns the absolute path of the skill directory this Skill was loaded
// from. It is the root used to resolve references, examples, scripts, and
// any filesystem-scoped built-in tools (for example those returned by
// [github.com/tsumina/dango/internal/llm/skill/builtin.All]).
func (s *Skill) Dir() string { return s.dir }

// BashAllow returns the executables this skill wants to permit on top of
// the built-in default bash allowlist. Callers pass it alongside
// [Skill.BashBlock] to
// [github.com/tsumina/dango/internal/llm/skill/builtin.WithAllowlistAdjust]
// when wiring the built-in tools.
func (s *Skill) BashAllow() []string { return append([]string(nil), s.bashAllow...) }

// BashBlock returns the executables this skill wants to remove from the
// built-in default bash allowlist. Entries in BashBlock override both the
// default list and [Skill.BashAllow].
func (s *Skill) BashBlock() []string { return append([]string(nil), s.bashBlock...) }

// Config configures how a [Skill] is loaded.
//
// Dir must point to a skill directory containing a [SkillFile]. Client is
// the LLM client the loaded Skill will be bound to and must be non-nil.
// BashAllow and BashBlock let callers narrow or widen the built-in bash
// allowlist; the effective set the built-in tools should honour is
// builtin.DefaultAllowlist ∪ BashAllow \ BashBlock. Config is the
// canonical input shape for callers (for example the orchestrate Executor)
// that wire a Skill into a larger runtime.
type Config struct {
	Dir       string
	Client    *llm.Client
	BashAllow []string
	BashBlock []string
}

// New loads a [Skill] from the directory described by cfg and binds it to
// cfg.Client.
//
// cfg.Dir must point to a directory containing a [SkillFile]. The
// frontmatter in that file is decoded into the Skill metadata fields and
// the remaining body is stored in Instruction. cfg.Client must be non-nil
// and is retained on the returned Skill so it can later invoke the LLM.
// Other entries in the directory such as scripts, references, and examples
// are not read here; callers that need them can resolve their own paths
// relative to [Skill.Dir].
func New(cfg Config) (*Skill, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("llm: skill requires a non-nil client")
	}

	info, err := os.Stat(cfg.Dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill path %q is not a directory", cfg.Dir)
	}

	skillPath := filepath.Join(cfg.Dir, SkillFile)
	file, err := os.Open(skillPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("skill directory %q is missing required %s: %w", cfg.Dir, SkillFile, err)
		}
		return nil, fmt.Errorf("open %s in %q: %w", SkillFile, cfg.Dir, err)
	}
	defer file.Close()

	var skill Skill
	rest, err := frontmatter.Parse(file, &skill)
	if err != nil {
		return nil, err
	}
	skill.Instruction = string(rest)
	skill.client = cfg.Client
	skill.dir = cfg.Dir
	skill.bashAllow = append([]string(nil), cfg.BashAllow...)
	skill.bashBlock = append([]string(nil), cfg.BashBlock...)
	return &skill, nil
}
