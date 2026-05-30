package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/llm/toolpolicy"
	"mvdan.cc/sh/v3/syntax"
)

// bashDefaultTimeout is the fallback timeout applied to bash commands when
// the caller does not specify timeout_seconds.
const bashDefaultTimeout = 60 * time.Second

// bashMaxOutputBytes caps the combined stdout+stderr returned by the bash
// tool. Longer output is truncated with a trailing notice so the model sees
// that data was dropped.
const bashMaxOutputBytes = 16 * 1024

// newBash returns a Tool that runs a shell command via /bin/bash -c with cwd
// fixed to the skill's temp playground and the parent process environment
// inherited.
//
// By default the tool enforces [defaultAllowlist]: every executable that
// appears in a simple command, pipeline, subshell, or command substitution
// must be on the list, otherwise the call is rejected before bash is
// invoked. Use [withAllowlist] to replace the list or [withoutAllowlist] to
// disable enforcement entirely.
//
// The command is bounded by timeout_seconds (default 60) and its combined
// stdout+stderr is captured in memory and capped at bashMaxOutputBytes. Two
// opt-in flags relax those defaults for skills that drive genuinely
// long-running or high-volume commands:
//
//   - long_running: when true, the timeout is not applied. Use this for HPC
//     job submission helpers, ML training, or any command the skill knows
//     may exceed the default bound. The parent context still cancels the
//     command, so the agent can still abort it.
//   - output_file: when set (a path resolved by the Skill workspace), combined
//     stdout+stderr is streamed directly to that file instead of being
//     returned to the model. The tool returns a short summary (path and byte
//     count) so the model can follow up with grep or read_file for the
//     sections it actually cares about, keeping its context small.
func newBash(ws workspace, opts ...option) tool {
	return newBashWithConfig(ws, newConfig(opts))
}

func newBashWithConfig(ws workspace, cfg *config) tool {
	allowlist := cfg.resolveAllowlist()
	return newFuncTool(
		"bash",
		"Run a shell command via /bin/bash -c inside the skill's private temp playground. Use for ad-hoc scripting, invoking helper programs, or generating temporary files. Returns combined stdout+stderr unless output_file is set. Commands are restricted to the configured allowlist (see defaultAllowlist). Redirection targets must be static and resolve inside the workspace; argument-level write targets (e.g. tee /etc/foo) are not validated.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to execute.",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "Optional timeout in seconds (default 60). Ignored when long_running is true.",
					"minimum":     1,
				},
				"long_running": map[string]any{
					"type":        "boolean",
					"description": "Set true for known long-running tasks (HPC jobs, ML training, polling loops). Disables the timeout while still honoring the parent context for cancellation.",
				},
				"output_file": map[string]any{
					"type":        "string",
					"description": "Optional output path. Relative paths resolve inside the temp playground; absolute paths must stay inside the temp playground, source workspace, or user-added accessible directories. When set, the tool returns a short summary instead of the raw output; use grep or read_file to extract sections from the file.",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},

		func(ctx context.Context, arguments string) (string, error) {
			var args struct {
				Command        string `json:"command"`
				TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
				LongRunning    bool   `json:"long_running,omitempty"`
				OutputFile     string `json:"output_file,omitempty"`
			}
			if err := json.Unmarshal([]byte(arguments), &args); err != nil {
				return "", fmt.Errorf("bash: parse arguments: %w", err)
			}
			if strings.TrimSpace(args.Command) == "" {
				return "", fmt.Errorf("bash: command is required")
			}
			if args.TimeoutSeconds < 0 {
				return "", fmt.Errorf("bash: timeout_seconds must be positive")
			}
			if allowlist != nil {
				if err := checkAllowlist(args.Command, allowlist); err != nil {
					return "", fmt.Errorf("bash: %w", err)
				}
			}
			if err := checkRedirections(args.Command, ws); err != nil {
				return "", fmt.Errorf("bash: %w", err)
			}
			if err := checkURLAllowlist(args.Command, cfg.bashURLAllowlist); err != nil {
				return "", fmt.Errorf("bash: %w", err)
			}
			if decision, matched, matchIndex, err := classifyBashCommandPolicy(args.Command, cfg.BashCommandPolicies); err != nil {
				return "", fmt.Errorf("bash: %w", err)
			} else if matched {
				if decision.Policy == ExecPolicyOff {
					toolpolicy.Record(ctx, decision)
					return "", &toolpolicy.DisabledError{
						Capability: decision.Capability,
						Reason:     decision.Reason,
					}
				}
				if decision.Policy == ExecPolicyNeedApprove {
					resp, err := toolpolicy.RequestApproval(ctx, toolpolicy.ApprovalRequest{
						Capability: decision.Capability,
						Policy:     decision.Policy,
						Reason:     decision.Reason,
					})
					decision.ApprovalOutcome = resp.Outcome
					decision.ApprovalReason = resp.Reason
					toolpolicy.Record(ctx, decision)
					if err != nil {
						return "", err
					}
					if resp.Outcome == toolpolicy.ApprovalOutcomeApproveForSession && matchIndex >= 0 {
						cfg.BashCommandPolicies[matchIndex].Policy = ExecPolicyPassby
					}
				} else {
					toolpolicy.Record(ctx, decision)
				}
			}

			// Long-running tasks opt out of the timeout entirely; all other
			// commands get bashDefaultTimeout unless overridden.
			cctx := ctx
			var cancel context.CancelFunc = func() {}
			var timeout time.Duration
			if !args.LongRunning {
				timeout = bashDefaultTimeout
				if args.TimeoutSeconds > 0 {
					timeout = time.Duration(args.TimeoutSeconds) * time.Second
				}
				cctx, cancel = context.WithTimeout(ctx, timeout)
			}
			defer cancel()

			cmd := exec.CommandContext(cctx, "/bin/bash", "-c", args.Command)
			cmd.Dir = ws.WorkDir()

			if args.OutputFile != "" {
				return runBashToFile(cctx, cmd, ws, args.OutputFile, timeout)
			}
			return runBashBuffered(cctx, cmd, timeout)
		},
	)
}

// checkRedirections parses command as a bash script and verifies that
// filesystem redirection targets are static paths contained by the workspace.
func checkRedirections(command string, ws workspace) error {
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return fmt.Errorf("parse command: %w", err)
	}
	var first error
	syntax.Walk(f, func(node syntax.Node) bool {
		if first != nil {
			return false
		}
		redir, ok := node.(*syntax.Redirect)
		if !ok {
			return true
		}
		if isHeredocRedirect(redir.Op) {
			return true
		}
		if redir.Word == nil {
			first = fmt.Errorf("redirection %s missing target", redir.Op)
			return false
		}
		target, ok := staticWordValue(redir.Word)
		if !ok {
			first = fmt.Errorf("redirection target for %s must be static, not dynamic", redir.Op)
			return false
		}
		if _, err := ws.ResolvePath(target); err != nil {
			first = fmt.Errorf("redirection target %q: %w", target, err)
			return false
		}
		return true
	})
	return first
}

func isHeredocRedirect(op syntax.RedirOperator) bool {
	switch op {
	case syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		return true
	default:
		return false
	}
}

// checkAllowlist parses command as a bash script and verifies that every
// simple command's head is in allow. It recurses into pipelines, subshells,
// and command substitutions so constructs like `foo | $(bar) && baz` are all
// checked. If the head is a dynamic expansion (e.g. $(...), $var), the call
// is rejected: allowing unknown dynamic heads would defeat the point of the
// allowlist.
func checkAllowlist(command string, allow map[string]struct{}) error {
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return fmt.Errorf("parse command: %w", err)
	}
	var first error
	syntax.Walk(f, func(node syntax.Node) bool {
		if first != nil {
			return false
		}
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		head, ok := staticWordValue(call.Args[0])
		if !ok {
			first = fmt.Errorf("dynamic command head is not allowed by the allowlist")
			return false
		}
		name := filepath.Base(head)
		if _, permitted := allow[name]; !permitted {
			first = fmt.Errorf("command %q is not in the allowlist", name)
			return false
		}
		return true
	})
	return first
}

// staticWordValue returns the literal string value of w when it consists
// only of literal and quoted-literal parts. It returns ok=false when the
// word contains dynamic expansions such as $var, `cmd`, or $(cmd).
func staticWordValue(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

func classifyBashCommandPolicy(command string, policies []BashCommandPolicy) (toolpolicy.Decision, bool, int, error) {
	if len(policies) == 0 {
		return toolpolicy.Decision{}, false, -1, nil
	}
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return toolpolicy.Decision{}, false, -1, fmt.Errorf("parse command: %w", err)
	}
	var matched toolpolicy.Decision
	var ok bool
	matchIndex := -1
	bestPrefixLen := -1
	syntax.Walk(f, func(node syntax.Node) bool {
		call, isCall := node.(*syntax.CallExpr)
		if !isCall || len(call.Args) == 0 {
			return true
		}
		head, args, static := staticCall(call)
		if !static {
			return true
		}
		normalized := normalizeCommandArgs(head, args)
		for i, policy := range policies {
			if filepath.Base(head) != filepath.Base(policy.Command) {
				continue
			}
			if len(policy.ArgsPrefix) > 0 && !equalArgsPrefix(normalized, policy.ArgsPrefix) {
				continue
			}
			if len(policy.ArgsPrefix) > len(normalized) {
				continue
			}
			candidate := toolpolicy.Decision{
				Capability: toolpolicy.BuiltinCapability("bash"),
				Policy:     policy.Policy.Default(),
				Reason:     fmt.Sprintf("matched bash command policy %q", strings.TrimSpace(strings.Join(append([]string{policy.Command}, policy.ArgsPrefix...), " "))),
			}
			prefixLen := len(policy.ArgsPrefix)
			if !ok || betterBashCommandPolicyMatch(candidate.Policy, prefixLen, i, matched, bestPrefixLen, matchIndex) {
				matched = candidate
				ok = true
				matchIndex = i
				bestPrefixLen = prefixLen
			}
		}
		return true
	})
	return matched, ok, matchIndex, nil
}

func betterBashCommandPolicyMatch(policy ExecPolicy, prefixLen int, index int, current toolpolicy.Decision, currentPrefixLen int, currentIndex int) bool {
	if rank := bashCommandPolicyRank(policy); rank != bashCommandPolicyRank(current.Policy) {
		return rank > bashCommandPolicyRank(current.Policy)
	}
	if prefixLen != currentPrefixLen {
		return prefixLen > currentPrefixLen
	}
	return currentIndex < 0 || index < currentIndex
}

func bashCommandPolicyRank(policy ExecPolicy) int {
	switch policy {
	case ExecPolicyOff:
		return 2
	case ExecPolicyNeedApprove:
		return 1
	default:
		return 0
	}
}

func staticCall(call *syntax.CallExpr) (string, []string, bool) {
	head, ok := staticWordValue(call.Args[0])
	if !ok {
		return "", nil, false
	}
	args := make([]string, 0, len(call.Args)-1)
	for _, arg := range call.Args[1:] {
		value, ok := staticWordValue(arg)
		if !ok {
			return "", nil, false
		}
		args = append(args, value)
	}
	return head, args, true
}

func normalizeCommandArgs(head string, args []string) []string {
	if filepath.Base(head) != "git" {
		return append([]string(nil), args...)
	}
	return trimLeadingGitOptions(args)
}

func trimLeadingGitOptions(args []string) []string {
	out := append([]string(nil), args...)
	for len(out) > 0 {
		switch token := out[0]; {
		case token == "--":
			return out[1:]
		case token == "-c",
			token == "-C",
			token == "--exec-path",
			token == "--git-dir",
			token == "--work-tree",
			token == "--namespace",
			token == "--config-env",
			token == "--super-prefix":
			if len(out) == 1 {
				return nil
			}
			out = out[2:]
		case strings.HasPrefix(token, "--git-dir="),
			strings.HasPrefix(token, "--work-tree="),
			strings.HasPrefix(token, "--namespace="),
			strings.HasPrefix(token, "--config-env="),
			strings.HasPrefix(token, "--super-prefix="),
			strings.HasPrefix(token, "--exec-path="):
			out = out[1:]
		case strings.HasPrefix(token, "-"):
			out = out[1:]
		default:
			return out
		}
	}
	return out
}

func equalArgsPrefix(args []string, prefix []string) bool {
	if len(prefix) > len(args) {
		return false
	}
	for i := range prefix {
		if args[i] != prefix[i] {
			return false
		}
	}
	return true
}

// runBashBuffered executes cmd, returning the captured combined output
// truncated to bashMaxOutputBytes. timeout is used only for error messages
// (zero means "no timeout configured").
func runBashBuffered(cctx context.Context, cmd *exec.Cmd, timeout time.Duration) (string, error) {
	out, err := cmd.CombinedOutput()
	output := truncateOutput(out, bashMaxOutputBytes)
	if cctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("bash: timed out after %s", timeout)
	}
	if err != nil {
		return output, fmt.Errorf("bash: %w", err)
	}
	return output, nil
}

// runBashToFile streams the command's combined stdout+stderr to outputFile
// (resolved against root) and returns a summary describing the file. This
// keeps large outputs out of the model's context; the model can grep or
// read_file the resulting file for just the sections it needs.
func runBashToFile(cctx context.Context, cmd *exec.Cmd, ws workspace, outputFile string, timeout time.Duration) (string, error) {
	p, err := ws.ResolvePath(outputFile)
	if err != nil {
		return "", fmt.Errorf("bash: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", fmt.Errorf("bash: %w", err)
	}
	f, err := os.Create(p)
	if err != nil {
		return "", fmt.Errorf("bash: %w", err)
	}
	defer f.Close()

	cmd.Stdout = f
	cmd.Stderr = f
	runErr := cmd.Run()

	info, statErr := os.Stat(p)
	var size int64
	if statErr == nil {
		size = info.Size()
	}
	summary := fmt.Sprintf("streamed output to %s (%d bytes)", outputFile, size)
	if cctx.Err() == context.DeadlineExceeded {
		return summary, fmt.Errorf("bash: timed out after %s", timeout)
	}
	if runErr != nil {
		return summary, fmt.Errorf("bash: %w", runErr)
	}
	return summary, nil
}

// truncateOutput returns out as a string, truncated to at most max bytes.
// When truncation happens a trailing notice reports the original size.
func truncateOutput(out []byte, max int) string {
	if len(out) <= max {
		return string(out)
	}
	return string(out[:max]) + fmt.Sprintf("\n... (output truncated, %d bytes total)", len(out))
}
