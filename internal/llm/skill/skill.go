package skill

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/adrg/frontmatter"
	"github.com/lithammer/shortuuid/v4"
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
// The zero value is not usable; construct instances with [NewFromDir] or
// [NewFromFS].
type Skill struct {
	Name        string `yaml:"name" toml:"name" json:"name"`
	Description string `yaml:"description" toml:"description" json:"description"`
	License     string `yaml:"license,omitempty" toml:"license,omitempty" json:"license,omitempty"`
	Instruction string

	dir       fs.FS
	envFiles  []string
	bashAllow []string
	bashBlock []string
	tools     []llm.Tool

	conv *llm.Conversation
}

// NewFromDir reads the [SkillFile] in dir and prepares a Skill using the
// host filesystem as its skill workspace.
//
// bashAllow and bashBlock are stored for callers that compose built-in bash
// tools around the skill. tools is the complete tool set advertised to the
// model when the skill is bound with [Skill.Bind]. Tool names must be unique
// and non-empty.
func NewFromDir(dir string, bashAllow []string, bashBlock []string, tools ...llm.Tool) (*Skill, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill path %q is not a directory", dir)
	}

	envFiles := []string(nil)
	envFile := filepath.Join(dir, ".env")
	if _, err := os.Stat(envFile); err == nil {
		envFiles = append(envFiles, envFile)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("skill: inspect env file %q: %w", envFile, err)
	}

	return newFromFS(os.DirFS(dir), dir, envFiles, bashAllow, bashBlock, tools...)
}

// NewFromFS reads [SkillFile] from fsys and prepares a Skill using fsys as its
// skill workspace.
//
// It is the filesystem-agnostic counterpart to [NewFromDir] and is intended
// for cases such as embedded skills that are packaged into the final binary.
// SKILL.md must be at the root of fsys.
func NewFromFS(fsys fs.FS, bashAllow []string, bashBlock []string, tools ...llm.Tool) (*Skill, error) {
	if fsys == nil {
		return nil, fmt.Errorf("skill: requires a non-nil filesystem")
	}
	return newFromFS(fsys, "skill filesystem", nil, bashAllow, bashBlock, tools...)
}

func newFromFS(fsys fs.FS, displayDir string, envFiles []string, bashAllow []string, bashBlock []string, tools ...llm.Tool) (*Skill, error) {
	if err := validateTools(tools); err != nil {
		return nil, err
	}
	skillPath := path.Join(".", SkillFile)
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
	sk.dir = fsys
	sk.envFiles = append([]string(nil), envFiles...)
	sk.bashAllow = append([]string(nil), bashAllow...)
	sk.bashBlock = append([]string(nil), bashBlock...)
	sk.tools = append([]llm.Tool(nil), tools...)

	return &sk, nil
}

// Bind returns a fresh runnable copy of s using the provided runtime wiring.
//
// Bind is the bridge between [NewFromDir]/[NewFromFS] and [Run]: callers can
// keep lightweight skills in registries and later bind a chosen LLM client and
// optional persistent session when they are ready to execute. When client is
// nil, Bind constructs one with [llm.NewClientFromEnv]; skills loaded from a
// host directory pass that directory's .env file when it exists.
func (s *Skill) Bind(client *llm.Client, cfg *llm.ConversationConfig, sessID *string, sessStores ...llm.SessionStore) (*Skill, error) {
	if s == nil {
		return nil, fmt.Errorf("skill: Bind requires a non-nil skill")
	}
	if client == nil {
		var err error
		client, err = llm.NewClientFromEnv(s.envFiles...)
		if err != nil {
			return nil, fmt.Errorf("skill: bind client from environment: %w", err)
		}
	}

	bound := *s
	bound.bashAllow = append([]string(nil), s.bashAllow...)
	bound.bashBlock = append([]string(nil), s.bashBlock...)
	bound.envFiles = append([]string(nil), s.envFiles...)
	bound.tools = append([]llm.Tool(nil), s.tools...)

	conv, err := llm.NewConversation(client, s.Instruction, bound.tools, cfg)
	if err != nil {
		return nil, err
	}
	if len(sessStores) > 0 || sessID != nil {
		id, err := resolveSessionID(context.Background(), sessID, sessStores)
		if err != nil {
			return nil, err
		}
		if err := conv.OpenSession(context.Background(), id, sessStores...); err != nil {
			return nil, fmt.Errorf("skill: open session %q: %w", id, err)
		}
	}
	bound.conv = conv
	return &bound, nil
}

func resolveSessionID(ctx context.Context, sessID *string, stores []llm.SessionStore) (string, error) {
	if len(stores) == 0 {
		return "", fmt.Errorf("skill: session id requires at least one session store")
	}
	if sessID == nil {
		return shortuuid.New(), nil
	}
	if *sessID == "" {
		return "", fmt.Errorf("skill: session id must not be empty")
	}
	for i, store := range stores {
		if store == nil {
			return "", fmt.Errorf("skill: session store %d is nil", i)
		}
		if _, err := store.Load(ctx, *sessID); err == nil {
			return *sessID, nil
		} else if !errors.Is(err, llm.ErrSessionNotFound) {
			return "", fmt.Errorf("skill: load session %q store %d: %w", *sessID, i, err)
		}
	}
	return "", fmt.Errorf("skill: session %q not found: %w", *sessID, llm.ErrSessionNotFound)
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

// Client returns the LLM client of the bound conversation, or nil when this
// Skill has not been bound yet.
func (s *Skill) Client() *llm.Client {
	if s == nil || s.conv == nil {
		return nil
	}
	return s.conv.Client()
}

// Dir returns the filesystem rooted at this skill's directory.
func (s *Skill) Dir() fs.FS { return s.dir }

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

// Run drives a single task to completion by delegating to
// [llm.Conversation.Run].
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
	if s == nil || s.conv == nil {
		return "", fmt.Errorf("skill: Run requires a bound conversation")
	}
	return s.conv.Run(ctx, userInput, effort)
}
