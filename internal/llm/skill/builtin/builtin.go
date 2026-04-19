// Package builtin provides the default set of filesystem and shell tools
// that dango skills expose to the LLM.
//
// Each tool is implemented in its own source file (for example [NewBash] in
// bash.go, [NewGrep] in grep.go) and returns a [llm.Tool] scoped to a
// workspace root. [All] composes the whole default set in the order agents
// see them, and [Option] values configure shared settings such as the bash
// allowlist.
//
// All filesystem tools resolve relative paths against the workspace root
// using [skill.ResolveWorkspacePath] and reject absolute paths or parent
// traversals that escape the root. The bash tool runs commands with the
// root as its working directory and enforces [DefaultAllowlist] unless
// overridden via [WithAllowlist] or [WithoutAllowlist].
package builtin

import "github.com/tsumina/dango/internal/llm"

// Option customizes the set of tools returned by [All] and the configuration
// of [NewBash]. The zero-value behaviour applies the [DefaultAllowlist] to
// the bash tool.
type Option func(*config)

// config holds the resolved settings shared by [All] and [NewBash].
type config struct {
	// bashAllowlist is the set of permitted executable basenames. When nil,
	// the default list is used (see [DefaultAllowlist]); when non-nil it
	// replaces the default in full. An empty map denies every command.
	bashAllowlist map[string]struct{}
	// bashAllowlistDisabled turns off allowlist enforcement entirely.
	bashAllowlistDisabled bool
}

func newConfig(opts []Option) *config {
	c := &config{}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// resolveAllowlist returns the effective allowlist set. A nil result means
// allowlist enforcement is disabled.
func (c *config) resolveAllowlist() map[string]struct{} {
	if c.bashAllowlistDisabled {
		return nil
	}
	if c.bashAllowlist != nil {
		return c.bashAllowlist
	}
	set := make(map[string]struct{}, len(DefaultAllowlist))
	for _, n := range DefaultAllowlist {
		set[n] = struct{}{}
	}
	return set
}

// WithAllowlist replaces the default bash allowlist with names. Passing an
// empty slice denies every command.
func WithAllowlist(names []string) Option {
	return func(c *config) {
		c.bashAllowlist = make(map[string]struct{}, len(names))
		c.bashAllowlistDisabled = false
		for _, n := range names {
			c.bashAllowlist[n] = struct{}{}
		}
	}
}

// WithAllowlistAdjust produces the effective bash allowlist by adjusting
// [DefaultAllowlist]: allow is added on top and block is removed afterwards,
// so the resulting set is DefaultAllowlist ∪ allow \ block. Either slice may
// be nil or empty. Entries in block win over allow, letting callers keep the
// broad default while trimming a few commands or adding skill-specific ones.
func WithAllowlistAdjust(allow, block []string) Option {
	return func(c *config) {
		set := make(map[string]struct{}, len(DefaultAllowlist)+len(allow))
		for _, n := range DefaultAllowlist {
			set[n] = struct{}{}
		}
		for _, n := range allow {
			set[n] = struct{}{}
		}
		for _, n := range block {
			delete(set, n)
		}
		c.bashAllowlist = set
		c.bashAllowlistDisabled = false
	}
}

// WithoutAllowlist disables bash allowlist enforcement entirely. Use with
// caution: the model may then invoke arbitrary executables.
func WithoutAllowlist() Option {
	return func(c *config) {
		c.bashAllowlist = nil
		c.bashAllowlistDisabled = true
	}
}

// All returns the default set of filesystem and shell tools scoped to root,
// in the order an agent sees them: bash first, then read/write/edit helpers,
// delete/move, list_dir, grep, and pwd.
//
// root should be an existing directory; typically it is the skill directory.
// Options apply to tools that honour shared configuration (currently only
// the bash allowlist).
func All(root string, opts ...Option) []llm.Tool {
	cfg := newConfig(opts)
	return []llm.Tool{
		newBashWithConfig(root, cfg),
		NewReadFile(root),
		NewWriteFile(root),
		NewEditFile(root),
		NewDeleteFile(root),
		NewMoveFile(root),
		NewListDir(root),
		NewGrep(root),
		NewPwd(root),
	}
}
