package llm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/adrg/frontmatter"
	"github.com/lithammer/shortuuid/v4"
)

// SkillFile is the required filename inside a skill directory that carries
// the skill's frontmatter metadata and prompt body.
const SkillFile = "SKILL.md"

// Skill is the service module for a single skill directory.
//
// A Skill bundles the metadata and instruction prompt loaded from the
// skill's SKILL.md with a multi-turn [Conversation] that runs that
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
	workspace *workspaceRoot
	envFiles  []string
	bashAllow []string
	bashBlock []string
	tools     []Tool

	conv *Conversation
}

// NewFromDir reads the [SkillFile] in dir and prepares a Skill using the
// host filesystem as its skill workspace.
//
// bashAllow and bashBlock are stored for callers that compose built-in bash
// tools around the skill. tools is the complete tool set advertised to the
// model when the skill is bound with [Skill.Bind]. Tool names must be unique
// and non-empty.
func NewFromDir(dir string, bashAllow []string, bashBlock []string, tools ...Tool) (*Skill, error) {
	workspace, err := newWorkspaceRoot(dir)
	if err != nil {
		return nil, err
	}

	envFiles := []string(nil)
	envFile := filepath.Join(workspace.SkillRoot(), ".env")
	if _, err := os.Stat(envFile); err == nil {
		envFiles = append(envFiles, envFile)
	} else if !os.IsNotExist(err) {
		_ = workspace.cleanup()
		return nil, fmt.Errorf("skill: inspect env file %q: %w", envFile, err)
	}

	sk, err := newFromFS(os.DirFS(workspace.SkillRoot()), workspace.SkillRoot(), workspace, envFiles, bashAllow, bashBlock, tools...)
	if err != nil {
		_ = workspace.cleanup()
		return nil, err
	}
	return sk, nil
}

// NewFromFS reads [SkillFile] from fsys and prepares a Skill using fsys as its
// skill workspace.
//
// It is the filesystem-agnostic counterpart to [NewFromDir] and is intended
// for cases such as embedded skills that are packaged into the final binary.
// SKILL.md must be at the root of fsys.
func NewFromFS(fsys fs.FS, bashAllow []string, bashBlock []string, tools ...Tool) (*Skill, error) {
	if fsys == nil {
		return nil, fmt.Errorf("skill: requires a non-nil filesystem")
	}
	workspace, err := newTempWorkspaceRoot()
	if err != nil {
		return nil, err
	}
	sk, err := newFromFS(fsys, "skill filesystem", workspace, nil, bashAllow, bashBlock, tools...)
	if err != nil {
		_ = workspace.cleanup()
		return nil, err
	}
	return sk, nil
}

func newFromFS(fsys fs.FS, displayDir string, workspace *workspaceRoot, envFiles []string, bashAllow []string, bashBlock []string, tools ...Tool) (*Skill, error) {
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
	sk.workspace = workspace
	sk.envFiles = append([]string(nil), envFiles...)
	sk.bashAllow = append([]string(nil), bashAllow...)
	sk.bashBlock = append([]string(nil), bashBlock...)
	sk.tools = append([]Tool(nil), tools...)

	return &sk, nil
}

// Bind returns a fresh runnable copy of s using the provided runtime wiring.
//
// Bind is the bridge between [NewFromDir]/[NewFromFS] and [Run]: callers can
// keep lightweight skills in registries and later bind a chosen LLM client and
// optional persistent session when they are ready to execute. When client is
// nil, Bind constructs one with [NewClientFromEnv]; skills loaded from a
// host directory pass that directory's .env file when it exists.
func (s *Skill) Bind(client *Client, cfg *ConversationConfig, sessID *string, sessStores ...SessionStore) (*Skill, error) {
	if s == nil {
		return nil, fmt.Errorf("skill: Bind requires a non-nil skill")
	}
	if client == nil {
		var err error
		client, err = NewClientFromEnv(s.envFiles...)
		if err != nil {
			return nil, fmt.Errorf("skill: bind client from environment: %w", err)
		}
	}

	bound := *s
	bound.bashAllow = append([]string(nil), s.bashAllow...)
	bound.bashBlock = append([]string(nil), s.bashBlock...)
	bound.envFiles = append([]string(nil), s.envFiles...)
	bound.tools = append([]Tool(nil), s.tools...)

	conv, err := NewConversation(client, s.runtimeInstruction(), bound.tools, cfg)
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

// WithAccessibleDirs configures the additional directories this skill's
// built-in tools may access in addition to the skill source root and temp
// playground.
//
// Each directory must already exist. Access is recursive: any file or
// subdirectory that remains inside one of these roots is accepted by the Skill
// workspace resolver. Relative tool paths still resolve inside [Skill.TempDir];
// use absolute paths to access these additional directories. Calling
// WithAccessibleDirs replaces the previous additional directory set; passing no
// directories removes them all. It must be called after construction and before
// [Skill.Bind].
func (s *Skill) WithAccessibleDirs(dirs ...string) error {
	if s == nil {
		return fmt.Errorf("skill: WithAccessibleDirs requires a non-nil skill")
	}
	if s.conv != nil {
		return fmt.Errorf("skill: WithAccessibleDirs requires an unbound skill")
	}
	if s.workspace == nil {
		return fmt.Errorf("skill: WithAccessibleDirs requires a skill workspace")
	}
	return s.workspace.setAccessibleDirs(dirs...)
}

func (s *Skill) copy() *Skill {
	bound := *s
	bound.bashAllow = append([]string(nil), s.bashAllow...)
	bound.bashBlock = append([]string(nil), s.bashBlock...)
	bound.envFiles = append([]string(nil), s.envFiles...)
	bound.tools = append([]Tool(nil), s.tools...)
	return &bound
}

func (s *Skill) runtimeInstruction() string {
	if s == nil {
		return ""
	}
	if s.workspace == nil {
		return s.Instruction
	}
	return appendWorkspaceInstruction(s.Instruction, s.workspace)
}

func appendWorkspaceInstruction(instruction string, workspace *workspaceRoot) string {
	if workspace == nil {
		return instruction
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(instruction, "\n"))
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("Workspace access:\n")
	if root := workspace.SkillRoot(); root != "" {
		fmt.Fprintf(&b, "- Source workspace: %s. Prefer this area for durable skill work when the task belongs with the skill. Use absolute paths under this directory.\n", root)
	}
	fmt.Fprintf(&b, "- Temp playground: %s. Relative file paths and shell commands run here. Use it for drafts, experiments, generated logs, and other disposable work.\n", workspace.TempRoot())
	if dirs := workspace.AccessibleDirs(); len(dirs) > 0 {
		b.WriteString("- User-added directories: use absolute paths under these roots exactly as requested by the user. Access is recursive within each root.\n")
		for _, dir := range dirs {
			fmt.Fprintf(&b, "  - %s\n", dir)
		}
	}
	b.WriteString("Do not access paths outside these roots.")
	return b.String()
}

func resolveSessionID(ctx context.Context, sessID *string, stores []SessionStore) (string, error) {
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
		} else if !errors.Is(err, ErrSessionNotFound) {
			return "", fmt.Errorf("skill: load session %q store %d: %w", *sessID, i, err)
		}
	}
	return "", fmt.Errorf("skill: session %q not found: %w", *sessID, ErrSessionNotFound)
}

func validateTools(tools []Tool) error {
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
func (s *Skill) Client() *Client {
	if s == nil || s.conv == nil {
		return nil
	}
	return s.conv.Client()
}

// Dir returns the filesystem rooted at this skill's directory.
func (s *Skill) Dir() fs.FS { return s.dir }

// WorkspaceRoot returns the host directory this skill was loaded from, or an
// empty string for skills loaded from an abstract filesystem.
func (s *Skill) WorkspaceRoot() string {
	if s == nil || s.workspace == nil {
		return ""
	}
	return s.workspace.SkillRoot()
}

// TempDir returns the private temporary playground allocated for this skill.
// Built-in tools use this directory as their working area for relative paths
// and shell execution.
func (s *Skill) TempDir() string {
	if s == nil || s.workspace == nil {
		return ""
	}
	return s.workspace.TempRoot()
}

// AccessibleDirs returns the additional host directories this skill's built-in
// tools may access beyond [Skill.WorkspaceRoot] and [Skill.TempDir].
func (s *Skill) AccessibleDirs() []string {
	if s == nil || s.workspace == nil {
		return nil
	}
	return s.workspace.AccessibleDirs()
}

// BashAllow returns the executables this skill wants to permit on top
// of the built-in default bash allowlist. Callers pass it alongside
// [Skill.BashBlock] to [Skill.BuiltinTools] when wiring the built-in tools.
func (s *Skill) BashAllow() []string { return append([]string(nil), s.bashAllow...) }

// BashBlock returns the executables this skill wants to remove from
// the built-in default bash allowlist. Entries in BashBlock override
// both the default list and [Skill.BashAllow].
func (s *Skill) BashBlock() []string { return append([]string(nil), s.bashBlock...) }

// Conversation returns the underlying [Conversation]. Callers may
// inspect its turns, usage, or session metadata but should not mutate
// it concurrently with a running [Skill.Run].
func (s *Skill) Conversation() *Conversation { return s.conv }

// Run drives a single task to completion by delegating to
// [Conversation.Run].
//
// userInput is appended as a user turn before the loop starts. The
// returned string is the concatenated output_text of the model's final
// response.
//
// effort overrides the reasoning-effort level for every request this
// Run issues, letting different driver stages (for example planning
// vs. execution) pick different levels without reconfiguring the
// underlying [Client]. Pass an empty string to use the level
// configured on the client.
func (s *Skill) Run(ctx context.Context, userInput string, effort ReasoningEffort) (string, error) {
	if s == nil || s.conv == nil {
		return "", fmt.Errorf("skill: Run requires a bound conversation")
	}
	return s.conv.Run(ctx, userInput, effort)
}
