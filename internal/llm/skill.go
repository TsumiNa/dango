package llm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/lithammer/shortuuid/v4"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/frontmatter"
	"github.com/tsumina/dango/internal/llm/internal/builtin"
)

// SkillFile is the required filename inside a skill directory that carries
// the skill's frontmatter metadata and prompt body.
const SkillFile = "SKILL.md"

// Skill is the service module for a single skill directory.
//
// A Skill bundles the metadata and instruction prompt loaded from the
// skill's SKILL.md with a multi-turn [Conversation] that runs that
// instruction against a configured set of tools. Callers drive it with
// [Skill.Run]; the bound skill owns the runtime stream, while the underlying
// conversation owns the request/tool-call loop and, when a session is
// configured, the append-only event log.
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
// The zero value is not usable; construct instances with [NewSkill].
type Skill struct {
	Name        string `yaml:"name" toml:"name" json:"name"`
	Description string `yaml:"description" toml:"description" json:"description"`
	License     string `yaml:"license,omitempty" toml:"license,omitempty" json:"license,omitempty"`
	Instruction string

	dir           fs.FS
	workspace     *workspaceRoot
	envFiles      []string
	bashAllow     []string
	bashBlock     []string
	builtinExtras []string
	tools         []Tool

	conv        *Conversation
	eventStream *streampkg.Stream
}

// SkillConfig controls optional behaviour while loading a [Skill].
type SkillConfig struct {
	// BashAllow lists command names allowed by built-in bash tools.
	BashAllow []string
	// BashBlock lists command names blocked by built-in bash tools.
	BashBlock []string
	// BuiltinExtras lists opt-in built-in tool names to append after the
	// default built-in tool set. Supported names include list_dir and pwd.
	BuiltinExtras []string
}

// DefaultSkillConfig returns the default optional behaviour for [NewSkill].
func DefaultSkillConfig() SkillConfig {
	return SkillConfig{}
}

// SkillOption adjusts a constructed lightweight [Skill] before it is returned.
type SkillOption func(*Skill) error

// WithTools appends tool implementations to the constructed Skill's initial
// tool set.
//
// The Skill keeps references to the supplied Tool values and may call them from
// a bound conversation while the skill is running. Callers remain responsible
// for any synchronization required by mutable tool implementations or by state
// captured in tool callbacks.
func WithTools(tools ...Tool) SkillOption {
	return func(s *Skill) error {
		combined := append([]Tool(nil), s.tools...)
		combined = append(combined, tools...)
		if err := validateTools(combined); err != nil {
			return err
		}
		s.tools = combined
		return nil
	}
}

// BindOption configures one [Skill.Bind] operation.
type BindOption func(*bindSettings)

type bindSettings struct {
	sessionID *string
	stores    []SessionStore
}

// WithNewSession opens a new persisted conversation session in stores while
// binding a Skill.
//
// The bound Skill's Conversation keeps references to the supplied stores and
// appends session events to them during later conversation mutations. Callers
// are responsible for synchronization if a store is shared concurrently unless
// the SessionStore implementation documents its own concurrency safety.
func WithNewSession(stores ...SessionStore) BindOption {
	return func(settings *bindSettings) {
		settings.sessionID = nil
		settings.stores = append([]SessionStore(nil), stores...)
	}
}

// WithExistingSession resumes sessionID from stores while binding a Skill.
//
// The bound Skill's Conversation keeps references to the supplied stores and
// appends session events to them during later conversation mutations. Callers
// are responsible for synchronization if a store is shared concurrently unless
// the SessionStore implementation documents its own concurrency safety.
func WithExistingSession(sessionID string, stores ...SessionStore) BindOption {
	return func(settings *bindSettings) {
		id := sessionID
		settings.sessionID = &id
		settings.stores = append([]SessionStore(nil), stores...)
	}
}

// NewSkill reads [SkillFile] from dir and prepares a lightweight Skill.
//
// cfg controls how the skill's built-in bash tools are later configured.
// options such as [WithTools] adjust the final lightweight skill instance
// before it is returned.
//
// When dir is a host path, NewSkill also records that directory's .env file when it
// exists and exposes the directory as the skill's source workspace. When dir
// implements [fs.FS], SKILL.md must be at its root.
//
// Accepted values are string-like host directory paths and values
// implementing [fs.FS].
func NewSkill(dir any, cfg SkillConfig, opts ...SkillOption) (*Skill, error) {
	if rawFS, ok := any(dir).(fs.FS); ok {
		if rawFS == nil {
			return nil, fmt.Errorf("skill: requires a non-nil filesystem")
		}
		workspace, err := newTempWorkspaceRoot()
		if err != nil {
			return nil, err
		}
		sk, err := newFromFS(rawFS, "skill filesystem", workspace, nil, cfg)
		if err != nil {
			_ = workspace.cleanup()
			return nil, err
		}
		if err := applySkillOptions(sk, opts); err != nil {
			_ = workspace.cleanup()
			return nil, err
		}
		return sk, nil
	}

	rawDir := reflect.ValueOf(any(dir))
	if !rawDir.IsValid() || rawDir.Kind() != reflect.String {
		return nil, fmt.Errorf("skill: unsupported skill dir type %T", dir)
	}
	workspace, err := newWorkspaceRoot(rawDir.String())
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

	sk, err := newFromFS(os.DirFS(workspace.SkillRoot()), workspace.SkillRoot(), workspace, envFiles, cfg)
	if err != nil {
		_ = workspace.cleanup()
		return nil, err
	}
	if err := applySkillOptions(sk, opts); err != nil {
		_ = workspace.cleanup()
		return nil, err
	}
	return sk, nil
}

func newFromFS(fs fs.FS, displayDir string, workspace *workspaceRoot, envFiles []string, cfg SkillConfig) (*Skill, error) {
	skillPath := path.Join(".", SkillFile)
	file, err := fs.Open(skillPath)
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
	sk.dir = fs
	sk.workspace = workspace
	sk.envFiles = append([]string(nil), envFiles...)
	sk.bashAllow = append([]string(nil), cfg.BashAllow...)
	sk.bashBlock = append([]string(nil), cfg.BashBlock...)
	sk.builtinExtras = append([]string(nil), cfg.BuiltinExtras...)

	return &sk, nil
}

func applySkillOptions(sk *Skill, opts []SkillOption) error {
	for i, opt := range opts {
		if opt == nil {
			return fmt.Errorf("skill: option %d is nil", i)
		}
		if err := opt(sk); err != nil {
			return err
		}
	}
	return nil
}

// Bind returns a runnable copy of s using the provided runtime wiring.
//
// [NewSkill] prepares the shared skill configuration: metadata, workspace access,
// env files, and tool set. Bind clones that configuration into a concrete,
// runnable instance with its own [Conversation]. When client is nil, Bind
// constructs one with [NewClientFromEnv]; skills loaded from a host directory
// pass that directory's .env file when it exists.
//
// [WithNewSession] opens a fresh persisted session. [WithExistingSession]
// resumes a session that already exists in at least one supplied store; it does
// not seed or create a new stored session for an explicit caller-provided id.
// If the id cannot be resolved from the supplied stores, Bind returns
// [ErrSessionNotFound].
func (s *Skill) Bind(client *Client, cfg ConversationConfig, opts ...BindOption) (*Skill, error) {
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

	bound := s.copy()

	runtimeCfg := cfg
	if runtimeCfg.StreamEvents {
		if runtimeCfg.EventStream == nil {
			runtimeCfg.EventStream = streampkg.New(runtimeCfg.StreamScope, streampkg.DefaultConfig())
		}
		bound.eventStream = runtimeCfg.EventStream
	}
	conv, err := NewConversation(client, s.runtimeInstruction(), bound.tools, runtimeCfg)
	if err != nil {
		return nil, err
	}
	settings := bindSettings{}
	for _, opt := range opts {
		if opt != nil {
			opt(&settings)
		}
	}
	if len(settings.stores) > 0 || settings.sessionID != nil {
		id, err := resolveSessionID(context.Background(), settings.sessionID, settings.stores)
		if err != nil {
			return nil, err
		}
		if err := conv.OpenSession(context.Background(), id, settings.stores...); err != nil {
			return nil, fmt.Errorf("skill: open session %q: %w", id, err)
		}
	}
	bound.conv = conv
	return bound, nil
}

// SetAccessibleDirs configures the additional directories this skill's
// built-in tools may access in addition to the skill source root and temp
// playground.
//
// Each directory must already exist. Access is recursive: any file or
// subdirectory that remains inside one of these roots is accepted by the Skill
// workspace resolver. Relative tool paths still resolve inside [Skill.TempDir];
// use absolute paths to access these additional directories. Calling
// SetAccessibleDirs replaces the previous additional directory set; passing no
// directories removes them all. It must be called after construction and before
// [Skill.Bind].
func (s *Skill) SetAccessibleDirs(dirs ...string) error {
	if s == nil {
		return fmt.Errorf("skill: SetAccessibleDirs requires a non-nil skill")
	}
	if s.conv != nil {
		return fmt.Errorf("skill: SetAccessibleDirs requires an unbound skill")
	}
	if s.workspace == nil {
		return fmt.Errorf("skill: SetAccessibleDirs requires a skill workspace")
	}
	return s.workspace.setAccessibleDirs(dirs...)
}

func (s *Skill) copy() *Skill {
	bound := *s
	bound.workspace = s.workspace.copy()
	bound.bashAllow = append([]string(nil), s.bashAllow...)
	bound.bashBlock = append([]string(nil), s.bashBlock...)
	bound.builtinExtras = append([]string(nil), s.builtinExtras...)
	bound.envFiles = append([]string(nil), s.envFiles...)
	bound.tools = append([]Tool(nil), s.tools...)
	bound.conv = nil
	bound.eventStream = nil
	return &bound
}

func (s *Skill) runtimeInstruction() string {
	if s == nil {
		return ""
	}
	body := prependSystemInstruction(s.Instruction)
	if s.workspace == nil {
		return body
	}
	return appendWorkspaceInstruction(body, s.workspace)
}

// prependSystemInstruction wraps the skill's own SKILL.md body with the
// platform-level conventions every skill needs (agent lifecycle, exchange
// markdown contract, built-in tool budget rules, etc.). The shared block
// always comes first so an external skill written without Dango knowledge
// inherits the cooperation rules; the skill-specific body, which describes
// *this* skill's job, refines and may override them.
func prependSystemInstruction(instruction string) string {
	system := strings.TrimSpace(builtin.SystemInstructions)
	if system == "" {
		return instruction
	}
	body := strings.TrimSpace(instruction)
	if body == "" {
		return system
	}
	return system + "\n\n" + body
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

// BuiltinExtras returns opt-in built-in tool names appended after the default
// built-in tool set.
func (s *Skill) BuiltinExtras() []string { return append([]string(nil), s.builtinExtras...) }

// Conversation returns the underlying [Conversation]. Callers may
// inspect its turns, usage, or session metadata but should not mutate
// it concurrently with a running [Skill.Run].
func (s *Skill) Conversation() *Conversation { return s.conv }

// EventStream returns the bound skill's runtime stream, or nil when the skill
// is unbound or was bound without stream events enabled.
func (s *Skill) EventStream() *streampkg.Stream {
	if s == nil {
		return nil
	}
	return s.eventStream
}

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
