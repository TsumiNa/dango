package builtin

// defaultAllowlist lists the executable names that the bash tool permits by
// default. It covers common read-only shell utilities, network fetchers, and
// the primary language / package-manager toolchains dango skills reach for.
// Destructive utilities such as rm, mv, sudo, and dd are intentionally
// omitted; use the delete_file or move_file tools for workspace-scoped
// deletion and renaming.
var defaultAllowlist = []string{
	// Read-only filesystem / text utilities.
	"cat", "head", "tail", "wc", "file", "stat", "ls", "find", "du", "df",
	"echo", "printf", "grep", "egrep", "fgrep", "sed", "awk", "cut",
	"sort", "uniq", "tr", "tee", "diff", "basename", "dirname", "realpath",
	// Directory / file creation (non-destructive in isolation).
	"mkdir", "touch", "cp", "ln",
	// Archives.
	"tar", "zip", "unzip", "gzip", "gunzip", "bzip2", "bunzip2", "xz", "unxz",
	// Shell utilities.
	"pwd", "env", "which", "date", "sleep", "xargs", "test", "true", "false",
	// Structured-data utilities.
	"jq", "yq",
	// Network fetchers.
	"curl", "wget",
	// Build tooling.
	"make", "cmake", "ninja",
	// Language toolchains & package managers.
	"python", "python3", "pip", "pip3",
	"node", "npm", "pnpm", "npx", "yarn",
	"go", "gofmt",
	"cargo", "rustc", "rustup",
	"uv", "uvx", "poetry", "pipx",
	"conda", "mamba", "micromamba", "pixi",
	"jupyter",
	// Shell recursion is allowed so scripts and one-liners work.
	"bash", "sh",
}
