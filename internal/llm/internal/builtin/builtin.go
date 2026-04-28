// Package builtin provides the default set of filesystem and shell tools that
// dango skills expose to the LLM.
//
// The package is internal to internal/llm. Its exported surface is deliberately
// narrow: [Tools] is the only entrypoint the parent llm package should need.
// Individual tool constructors and allowlist configuration stay package-local
// so callers outside llm cannot assemble or depend on tool internals directly.
//
// All filesystem tools receive a workspace from the parent llm package and
// use that workspace to resolve paths before touching the host filesystem.
// Relative paths resolve inside the skill's private temp playground, while
// absolute paths are accepted only when they stay inside the skill source root,
// that temp playground, or user-added accessible directories. The bash tool
// runs commands in the temp playground and enforces the package default
// allowlist after applying per-skill allow/block entries.
package builtin

type option func(*config)

type config struct {
	// bashAllowlist is the set of permitted executable basenames. When nil,
	// the default list is used; when non-nil it
	// replaces the default in full. An empty map denies every command.
	bashAllowlist map[string]struct{}
	// bashAllowlistDisabled turns off allowlist enforcement entirely.
	bashAllowlistDisabled bool
}

func newConfig(opts []option) *config {
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
	set := make(map[string]struct{}, len(defaultAllowlist))
	for _, n := range defaultAllowlist {
		set[n] = struct{}{}
	}
	return set
}

func withAllowlist(names []string) option {
	return func(c *config) {
		c.bashAllowlist = make(map[string]struct{}, len(names))
		c.bashAllowlistDisabled = false
		for _, n := range names {
			c.bashAllowlist[n] = struct{}{}
		}
	}
}

func withAllowlistAdjust(allow, block []string) option {
	return func(c *config) {
		set := make(map[string]struct{}, len(defaultAllowlist)+len(allow))
		for _, n := range defaultAllowlist {
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

func withoutAllowlist() option {
	return func(c *config) {
		c.bashAllowlist = nil
		c.bashAllowlistDisabled = true
	}
}

// Tools returns the default set of filesystem and shell tools scoped to ws,
// in the order an agent sees them: bash first, then read/write/edit helpers,
// delete/move, list_dir, grep, and pwd.
//
// bashAllow is added to the default bash allowlist, and bashBlock is removed
// afterwards. Entries in bashBlock win when a name appears in both slices.
func Tools(ws workspace, bashAllow []string, bashBlock []string) []tool {
	cfg := newConfig([]option{withAllowlistAdjust(bashAllow, bashBlock)})
	return []tool{
		newBashWithConfig(ws, cfg),
		newReadFile(ws),
		newWriteFile(ws),
		newEditFile(ws),
		newDeleteFile(ws),
		newMoveFile(ws),
		newListDir(ws),
		newGrep(ws),
		newPwd(ws),
	}
}
