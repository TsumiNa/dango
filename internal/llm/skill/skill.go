package skill

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"

	"github.com/adrg/frontmatter"
	"github.com/tsumina/dango/internal/llm"
)

// SkillFile is the required filename inside a skill directory that carries
// the skill's frontmatter metadata and prompt body.
const SkillFile = "SKILL.md"

// DefaultMaxSteps re-exports [llm.DefaultMaxSteps] so skill callers do
// not need a direct dependency on the llm package for the common case.
const DefaultMaxSteps = llm.DefaultMaxSteps

// Skill is the service module for a single skill directory.
//
// A Skill bundles the metadata and instruction prompt loaded from the
// skill's SKILL.md with a multi-turn [llm.Conversation] that runs that
// instruction against a configured set of tools. Callers drive it with
// [Skill.Run]; the underlying conversation owns the request/tool-call
// loop and, when a session is configured, the append-only event log.
//
// A skill directory is laid out as:
//
//	<dir>/
//	    SKILL.md       // required: frontmatter + prompt body
//	    scripts/       // optional
//	    references/    // optional
//	    examples/      // optional
//
// The frontmatter at the top of SKILL.md populates the metadata fields,
// and the remaining body becomes [Skill.Instruction], which is used as
// the system prompt of the conversation.
//
// The zero value is not usable; construct instances with [New].
type Skill struct {
	Name        string `yaml:"name" toml:"name" json:"name"`
	Description string `yaml:"description" toml:"description" json:"description"`
	License     string `yaml:"license,omitempty" toml:"license,omitempty" json:"license,omitempty"`
	Instruction string

	dir       string
	client    *llm.Client
	bashAllow []string
	bashBlock []string

	conv      *llm.Conversation
	sessStore llm.SessionStore
	sessID    string
}

// Config configures how a [Skill] is loaded and wired for execution.
//
// Dir must point to a skill directory containing a [SkillFile]. Client
// is the LLM client the loaded Skill binds to and must be non-nil.
//
// BashAllow and BashBlock let callers narrow or widen the built-in bash
// allowlist; the effective set the built-in tools should honour is
// builtin.DefaultAllowlist ∪ BashAllow \ BashBlock.
//
// Tools lists the tools the Skill's conversation will dispatch during
// [Skill.Run]. Names must be unique and non-empty.
//
// MaxSteps overrides the conversation iteration bound. Values less than
// or equal to zero fall back to [llm.DefaultMaxSteps].
//
// AutoTrim, when non-nil, enables automatic history shrinking on the
// conversation. Summarizer optionally collapses dropped history into a
// single summary turn; without one the shrink pass simply trims.
//
// SessionStore and SessionID, when both set, bind the Skill to a
// persistent session that is opened lazily on the first [Skill.Run].
type Config struct {
	Dir       string
	Client    *llm.Client
	BashAllow []string
	BashBlock []string

	Tools      []llm.Tool
	MaxSteps   int
	AutoTrim   *llm.AutoShrinkConfig
	Summarizer llm.Summarizer

	SessionStore llm.SessionStore
	SessionID    string
}

// RuntimeConfig configures the execution-time pieces bound onto an existing
// loaded [Skill].
//
// It mirrors the runtime-oriented subset of [Config] so lightweight skills
// returned by [Load] can later be turned into runnable copies without reading
// [SkillFile] again.
type RuntimeConfig struct {
	Client *llm.Client

	Tools      []llm.Tool
	MaxSteps   int
	AutoTrim   *llm.AutoShrinkConfig
	Summarizer llm.Summarizer

	SessionStore llm.SessionStore
	SessionID    string
}

// Load reads the [SkillFile] in dir and parses its metadata and
// instruction body into a [Skill] without binding an LLM client or
// conversation. The returned Skill is lightweight and useful for
// discovering and inspecting skills, but cannot be executed.
func Load(dir string) (*Skill, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill path %q is not a directory", dir)
	}

	return loadFromFS(os.DirFS(dir), ".", dir, dir)
}

// LoadFS reads the [SkillFile] in dir from fsys and returns the resulting
// lightweight [Skill].
//
// It is the filesystem-agnostic counterpart to [Load] and is intended for
// cases such as embedded skills that are packaged into the final binary.
// Skills loaded from non-local filesystems do not expose a host directory, so
// [Skill.Dir] returns an empty string.
func LoadFS(fsys fs.FS, dir string) (*Skill, error) {
	if fsys == nil {
		return nil, fmt.Errorf("skill: requires a non-nil filesystem")
	}
	info, err := fs.Stat(fsys, dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill path %q is not a directory", dir)
	}
	return loadFromFS(fsys, dir, dir, "")
}

func loadFromFS(fsys fs.FS, dir string, displayDir string, hostDir string) (*Skill, error) {
	skillPath := path.Join(dir, SkillFile)
	file, err := fsys.Open(skillPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("skill directory %q is missing required %s: %w", displayDir, SkillFile, err)
		}
		return nil, fmt.Errorf("open %s in %q: %w", SkillFile, displayDir, err)
	}
	defer file.Close()

	var sk Skill
	rest, err := frontmatter.Parse(file, &sk)
	if err != nil {
		return nil, err
	}
	sk.Instruction = string(rest)
	sk.dir = hostDir

	return &sk, nil
}

// New loads a [Skill] from cfg.Dir, binds it to cfg.Client, and builds
// the conversation that [Skill.Run] will drive.
//
// cfg.Dir must point to a directory containing a [SkillFile]. The
// frontmatter in that file is decoded into the Skill metadata fields
// and the remaining body is stored in [Skill.Instruction]. cfg.Client
// must be non-nil; tool names in cfg.Tools must be unique and non-empty
// so misconfigured tool sets fail fast rather than silently shadowing
// each other at call time.
//
// Other entries in the directory such as scripts, references, and
// examples are not read here; callers that need them can resolve their
// own paths relative to [Skill.Dir].
func New(cfg Config) (*Skill, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("skill: requires a non-nil client")
	}

	sk, err := Load(cfg.Dir)
	if err != nil {
		return nil, err
	}

	sk.bashAllow = append([]string(nil), cfg.BashAllow...)
	sk.bashBlock = append([]string(nil), cfg.BashBlock...)

	return sk.Bind(RuntimeConfig{
		Client:       cfg.Client,
		Tools:        cfg.Tools,
		MaxSteps:     cfg.MaxSteps,
		AutoTrim:     cfg.AutoTrim,
		Summarizer:   cfg.Summarizer,
		SessionStore: cfg.SessionStore,
		SessionID:    cfg.SessionID,
	})
}

// Bind returns a fresh runnable copy of s using cfg for its runtime wiring.
//
// Bind is the bridge between [Load] and [Run]: callers can keep lightweight
// skills in registries and later bind a chosen LLM client, tool set, and
// optional session configuration when they are ready to execute.
func (s *Skill) Bind(cfg RuntimeConfig) (*Skill, error) {
	if s == nil {
		return nil, fmt.Errorf("skill: Bind requires a non-nil skill")
	}
	if cfg.Client == nil {
		return nil, fmt.Errorf("skill: Bind requires a non-nil client")
	}
	if err := validateTools(cfg.Tools); err != nil {
		return nil, err
	}

	bound := *s
	bound.client = cfg.Client
	bound.bashAllow = append([]string(nil), s.bashAllow...)
	bound.bashBlock = append([]string(nil), s.bashBlock...)
	bound.conv = llm.NewConversation(cfg.Client, s.Instruction, cfg.Tools)
	if cfg.MaxSteps > 0 {
		bound.conv.SetMaxSteps(cfg.MaxSteps)
	}
	if cfg.AutoTrim != nil {
		bound.conv.SetAutoShrink(*cfg.AutoTrim)
	}
	if cfg.Summarizer != nil {
		bound.conv.SetSummarizer(cfg.Summarizer)
	}
	bound.sessStore = cfg.SessionStore
	bound.sessID = cfg.SessionID
	return &bound, nil
}

func validateTools(tools []llm.Tool) error {
	seen := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		if t == nil {
			return fmt.Errorf("skill: received nil tool")
		}
		name := t.Name()
		if name == "" {
			return fmt.Errorf("skill: tool has empty name")
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("skill: duplicate tool name %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// Client returns the LLM client this skill is bound to.
func (s *Skill) Client() *llm.Client { return s.client }

// Dir returns the host filesystem path of the skill directory this Skill was
// loaded from.
//
// It is the root used to resolve references, examples, scripts, and any
// filesystem-scoped built-in tools (for example those returned by
// [github.com/tsumina/dango/internal/llm/skill/builtin.All]). Skills loaded
// from non-local filesystems, such as via [LoadFS], return an empty string.
func (s *Skill) Dir() string { return s.dir }

// BashAllow returns the executables this skill wants to permit on top
// of the built-in default bash allowlist. Callers pass it alongside
// [Skill.BashBlock] to
// [github.com/tsumina/dango/internal/llm/skill/builtin.WithAllowlistAdjust]
// when wiring the built-in tools.
func (s *Skill) BashAllow() []string { return append([]string(nil), s.bashAllow...) }

// BashBlock returns the executables this skill wants to remove from
// the built-in default bash allowlist. Entries in BashBlock override
// both the default list and [Skill.BashAllow].
func (s *Skill) BashBlock() []string { return append([]string(nil), s.bashBlock...) }

// Conversation returns the underlying [llm.Conversation]. Callers may
// inspect its turns, usage, or session metadata but should not mutate
// it concurrently with a running [Skill.Run].
func (s *Skill) Conversation() *llm.Conversation { return s.conv }

// Run drives a single task to completion. It lazily binds the
// conversation to the configured session on the first call when
// [Config.SessionStore] and [Config.SessionID] were provided, then
// delegates to [llm.Conversation.Run].
//
// userInput is appended as a user turn before the loop starts. The
// returned string is the concatenated output_text of the model's final
// response.
//
// effort overrides the reasoning-effort level for every request this
// Run issues, letting different driver stages (for example planning
// vs. execution) pick different levels without reconfiguring the
// underlying [llm.Client]. Pass an empty string to use the level
// configured on the client.
func (s *Skill) Run(ctx context.Context, userInput string, effort llm.ReasoningEffort) (string, error) {
	if s.sessStore != nil && s.conv.SessionID() == "" {
		if err := s.conv.OpenSession(ctx, s.sessStore, s.sessID); err != nil {
			return "", fmt.Errorf("skill: open session %q: %w", s.sessID, err)
		}
	}
	return s.conv.Run(ctx, userInput, effort)
}
