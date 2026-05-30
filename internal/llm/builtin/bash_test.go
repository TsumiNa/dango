package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsumina/dango/internal/llm/toolpolicy"
)

func TestBashRunsInRoot(t *testing.T) {
	root := t.TempDir()
	tool := newBash(testWorkspace{root})
	out, err := tool.Execute(context.Background(), `{"command": "pwd"}`)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	// macOS /var vs /private/var: compare canonicalized paths.
	wantRoot, _ := filepath.EvalSymlinks(root)
	gotRoot, _ := filepath.EvalSymlinks(strings.TrimSpace(out))
	if gotRoot != wantRoot {
		t.Errorf("bash pwd = %q, want %q", gotRoot, wantRoot)
	}
}

func TestBashEmptyCommand(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	if _, err := tool.Execute(context.Background(), `{"command": "  "}`); err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestBashInvalidJSON(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	if _, err := tool.Execute(context.Background(), `not json`); err == nil {
		t.Fatal("expected error for invalid JSON arguments")
	}
}

func TestBashTimeout(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	args, _ := json.Marshal(map[string]any{
		"command":         "sleep 2",
		"timeout_seconds": 1,
	})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected timeout error")
	} else if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout in error, got %v", err)
	}
}

func TestBashOutputTruncation(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	// Produce more than bashMaxOutputBytes by printing a big block.
	args, _ := json.Marshal(map[string]any{
		"command": "head -c 40000 /dev/zero | tr '\\0' 'a'",
	})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !strings.Contains(out, "output truncated") {
		t.Errorf("expected truncation notice, got %d bytes", len(out))
	}
	if len(out) > bashMaxOutputBytes+200 {
		t.Errorf("output not truncated: %d bytes", len(out))
	}
}

func TestBashLongRunningSkipsTimeout(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	// 2s sleep with a 1s timeout would normally fail; long_running skips it.
	args, _ := json.Marshal(map[string]any{
		"command":         "sleep 2 && echo done",
		"timeout_seconds": 1,
		"long_running":    true,
	})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("long_running bash: %v", err)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("expected 'done' in output, got %q", out)
	}
}

func TestBashOutputFileStreams(t *testing.T) {
	root := t.TempDir()
	tool := newBash(testWorkspace{root})
	args, _ := json.Marshal(map[string]any{
		"command":     "printf 'line1\\nline2\\nline3\\n'",
		"output_file": "logs/run.log",
	})
	summary, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("bash output_file: %v", err)
	}
	if !strings.Contains(summary, "logs/run.log") {
		t.Errorf("summary missing path: %q", summary)
	}
	data, err := os.ReadFile(filepath.Join(root, "logs/run.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(data) != "line1\nline2\nline3\n" {
		t.Errorf("log contents = %q", string(data))
	}
}

func TestBashOutputFileRejectsEscape(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	args, _ := json.Marshal(map[string]any{
		"command":     "echo hi",
		"output_file": "../escape.log",
	})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected escape rejection for output_file")
	}
}

func TestBashOutputFileCapturesLargeOutputWithoutTruncation(t *testing.T) {
	root := t.TempDir()
	tool := newBash(testWorkspace{root})
	// 40000 bytes would be truncated in buffered mode; file mode keeps all of it.
	args, _ := json.Marshal(map[string]any{
		"command":     "head -c 40000 /dev/zero | tr '\\0' 'a'",
		"output_file": "big.log",
	})
	summary, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if strings.Contains(summary, "truncated") {
		t.Errorf("summary should not mention truncation: %q", summary)
	}
	info, err := os.Stat(filepath.Join(root, "big.log"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != 40000 {
		t.Errorf("log size = %d, want 40000", info.Size())
	}
}

func TestBashRedirectionRejectsAbsoluteEscape(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	args, _ := json.Marshal(map[string]any{
		"command": "echo x > /tmp/foo",
	})
	_, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected absolute redirection escape rejection")
	}
	if !strings.Contains(err.Error(), "redirection target") {
		t.Errorf("expected redirection error, got %v", err)
	}
}

func TestBashRedirectionAllowsRelativeInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	tool := newBash(testWorkspace{root})
	args, _ := json.Marshal(map[string]any{
		"command": "echo x > out.txt",
	})
	if _, err := tool.Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("bash redirection: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "out.txt"))
	if err != nil {
		t.Fatalf("read redirected output: %v", err)
	}
	if string(data) != "x\n" {
		t.Errorf("redirected output = %q, want %q", string(data), "x\n")
	}
}

func TestBashRedirectionAllowsAbsoluteInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	tool := newBash(testWorkspace{root})
	outPath := filepath.Join(root, "out.txt")
	args, _ := json.Marshal(map[string]any{
		"command": "echo x > " + outPath,
	})
	if _, err := tool.Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("bash redirection: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read redirected output: %v", err)
	}
	if string(data) != "x\n" {
		t.Errorf("redirected output = %q, want %q", string(data), "x\n")
	}
}

func TestBashRedirectionRejectsDynamicTarget(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	args, _ := json.Marshal(map[string]any{
		"command": "echo x > $OUT",
	})
	_, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected dynamic redirection target rejection")
	}
	if !strings.Contains(err.Error(), "dynamic") {
		t.Errorf("expected dynamic error, got %v", err)
	}
}

func TestBashRedirectionRejectsCommandSubstitutionTarget(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	args, _ := json.Marshal(map[string]any{
		"command": "echo x > $(echo path)",
	})
	_, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected command substitution redirection target rejection")
	}
	if !strings.Contains(err.Error(), "dynamic") {
		t.Errorf("expected dynamic error, got %v", err)
	}
}

func TestBashRedirectionAllowsHeredoc(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	args, _ := json.Marshal(map[string]any{
		"command": "cat <<'EOF'\nhello\nEOF",
	})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("bash heredoc: %v", err)
	}
	if out != "hello\n" {
		t.Errorf("heredoc output = %q, want %q", out, "hello\n")
	}
}

func TestBashRedirectionAllowsAppend(t *testing.T) {
	root := t.TempDir()
	tool := newBash(testWorkspace{root})
	args, _ := json.Marshal(map[string]any{
		"command": "echo a >> out.txt",
	})
	if _, err := tool.Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("bash append redirection: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "out.txt"))
	if err != nil {
		t.Fatalf("read redirected output: %v", err)
	}
	if string(data) != "a\n" {
		t.Errorf("redirected output = %q, want %q", string(data), "a\n")
	}
}

func TestBashStderrRedirectionChecked(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	args, _ := json.Marshal(map[string]any{
		"command": "echo err 2> /tmp/escape",
	})
	_, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected stderr redirection escape rejection")
	}
	if !strings.Contains(err.Error(), "redirection target") {
		t.Errorf("expected redirection error, got %v", err)
	}
}

func TestBashAllowlistBlocksRm(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	args, _ := json.Marshal(map[string]any{"command": "rm -rf /"})
	_, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected allowlist rejection for rm")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("expected allowlist error, got %v", err)
	}
}

func TestBashAllowlistBlocksInPipeline(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	// echo is allowed; mv is not. The whole command must be rejected.
	args, _ := json.Marshal(map[string]any{"command": "echo hi && mv a b"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected pipeline allowlist rejection")
	}
}

func TestBashAllowlistBlocksInSubshell(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	args, _ := json.Marshal(map[string]any{"command": "echo $(rm /tmp/x)"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected subshell allowlist rejection")
	}
}

func TestBashAllowlistBlocksDynamicHead(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	// Dynamic head such as $CMD cannot be statically checked; reject it.
	args, _ := json.Marshal(map[string]any{"command": "$CMD hello"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected dynamic-head rejection")
	}
}

func TestBashAllowlistAcceptsPathedExecutable(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	// basename("/bin/echo") == "echo" which is in the default allowlist.
	args, _ := json.Marshal(map[string]any{"command": "/bin/echo ok"})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("echo output = %q", out)
	}
}

func TestBashAllowlistCustom(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()}, withAllowlist([]string{"rm"}))
	// With a custom allowlist, rm is now permitted but echo is not.
	args, _ := json.Marshal(map[string]any{"command": "echo hi"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected echo to be rejected by custom allowlist")
	}
}

func TestBashAllowlistDisabled(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()}, withoutAllowlist())
	// Without enforcement, any command is accepted by the allowlist check
	// (the command itself may still fail, but not due to allowlist).
	args, _ := json.Marshal(map[string]any{"command": "true"})
	if _, err := tool.Execute(context.Background(), string(args)); err != nil {
		t.Errorf("expected success with allowlist disabled, got %v", err)
	}
}

func TestBashAllowlistAllowsEnvPrefix(t *testing.T) {
	tool := newBash(testWorkspace{t.TempDir()})
	args, _ := json.Marshal(map[string]any{"command": "FOO=bar echo ok"})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("env-prefixed echo should be allowed: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("env-prefixed echo output = %q", out)
	}
}

func TestBashCommandPatternMatchSemantics(t *testing.T) {
	policies := []BashCommandPolicy{{Command: "git", ArgsPrefix: []string{"push"}, Policy: ExecPolicyNeedApprove}}
	for _, tc := range []struct {
		command string
		match   bool
	}{
		{command: "git push origin main", match: true},
		{command: "git -c user.name=test push origin main", match: true},
		{command: "git push-mirror origin main", match: false},
	} {
		decision, matched, _, err := classifyBashCommandPolicy(tc.command, policies)
		if err != nil {
			t.Fatalf("classifyBashCommandPolicy(%q): %v", tc.command, err)
		}
		if matched != tc.match {
			t.Fatalf("classifyBashCommandPolicy(%q) matched = %v, want %v (decision=%+v)", tc.command, matched, tc.match, decision)
		}
	}
}

func TestBashCommandPatternMostRestrictiveMatchWinsAcrossScript(t *testing.T) {
	policies := []BashCommandPolicy{
		{Command: "git", Policy: ExecPolicyPassby},
		{Command: "git", ArgsPrefix: []string{"push"}, Policy: ExecPolicyOff},
	}
	decision, matched, matchIndex, err := classifyBashCommandPolicy("git status && git push origin main", policies)
	if err != nil {
		t.Fatalf("classifyBashCommandPolicy: %v", err)
	}
	if !matched {
		t.Fatal("expected a policy match")
	}
	if decision.Policy != ExecPolicyOff {
		t.Fatalf("decision.Policy = %q, want off", decision.Policy)
	}
	if matchIndex != 1 {
		t.Fatalf("matchIndex = %d, want 1", matchIndex)
	}
}

func TestBashCommandPatternOffRejects(t *testing.T) {
	cfg := newConfig(nil)
	cfg.BashCommandPolicies = []BashCommandPolicy{{Command: "git", ArgsPrefix: []string{"push"}, Policy: ExecPolicyOff}}
	tool := newBashWithConfig(testWorkspace{t.TempDir()}, cfg)
	args, _ := json.Marshal(map[string]any{"command": "git push origin main"})
	_, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected off-pattern rejection")
	}
	var disabled *toolpolicy.DisabledError
	if !errors.As(err, &disabled) {
		t.Fatalf("expected DisabledError, got %v", err)
	}
}

func TestBashCommandPatternNeedApproveDeniesWithoutApprover(t *testing.T) {
	cfg := newConfig(nil)
	cfg.BashCommandPolicies = []BashCommandPolicy{{Command: "echo", ArgsPrefix: []string{"hello"}, Policy: ExecPolicyNeedApprove}}
	tool := newBashWithConfig(testWorkspace{t.TempDir()}, cfg)
	args, _ := json.Marshal(map[string]any{"command": "echo hello world"})
	_, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected approval denial")
	}
	var denied *toolpolicy.ApprovalDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected ApprovalDeniedError, got %v", err)
	}
}

func TestBashCommandPatternApproveForSessionDowngradesRuntimePolicy(t *testing.T) {
	cfg := newConfig(nil)
	cfg.BashCommandPolicies = []BashCommandPolicy{{Command: "echo", ArgsPrefix: []string{"hello"}, Policy: ExecPolicyNeedApprove}}
	tool := newBashWithConfig(testWorkspace{t.TempDir()}, cfg)
	args, _ := json.Marshal(map[string]any{"command": "echo hello world"})

	var approvals int
	ctx := toolpolicy.WithApprover(context.Background(), func(context.Context, toolpolicy.ApprovalRequest) (toolpolicy.ApprovalResponse, error) {
		approvals++
		return toolpolicy.ApprovalResponse{Outcome: toolpolicy.ApprovalOutcomeApproveForSession, Reason: "session ok"}, nil
	})

	if out, err := tool.Execute(ctx, string(args)); err != nil {
		t.Fatalf("first Execute: %v", err)
	} else if !strings.Contains(out, "hello world") {
		t.Fatalf("first output = %q, want hello world", out)
	}
	if out, err := tool.Execute(ctx, string(args)); err != nil {
		t.Fatalf("second Execute: %v", err)
	} else if !strings.Contains(out, "hello world") {
		t.Fatalf("second output = %q, want hello world", out)
	}
	if approvals != 1 {
		t.Fatalf("approvals = %d, want 1", approvals)
	}
}

// TestBashAllowsGitVersion confirms that "git" is on the allowlist and that
// `git --version` runs successfully through the bash tool.
func TestBashAllowsGitVersion(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH; skipping allowlist integration test")
	}
	tool := newBash(testWorkspace{t.TempDir()})
	args, _ := json.Marshal(map[string]any{"command": "git --version"})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("git --version: %v", err)
	}
	if !strings.Contains(out, "git") {
		t.Errorf("unexpected output from git --version: %q", out)
	}
}

// TestBashAllowsGitLogInsideWorkspace initialises a git repo in the workspace,
// commits a file, and verifies that `git -C <root> log -1 --format=%s`
// returns the commit subject.
func TestBashAllowsGitLogInsideWorkspace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH; skipping allowlist integration test")
	}
	root := t.TempDir()
	ws := testWorkspace{root}

	// Set up a minimal git repo inside the workspace.
	setup := []string{
		"git init " + root,
		"git -C " + root + " config user.email test@example.com",
		"git -C " + root + " config user.name Test",
		"touch " + filepath.Join(root, "file.txt"),
		"git -C " + root + " add file.txt",
		"git -C " + root + " commit -m 'initial commit'",
	}
	for _, cmd := range setup {
		tool := newBash(ws, withoutAllowlist())
		setupArgs, _ := json.Marshal(map[string]any{"command": cmd})
		if _, err := tool.Execute(context.Background(), string(setupArgs)); err != nil {
			t.Fatalf("setup %q: %v", cmd, err)
		}
	}

	// Now use the default allowlist tool to run git log.
	tool := newBash(ws)
	args, _ := json.Marshal(map[string]any{
		"command": "git -C " + root + " log -1 --format=%s",
	})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if !strings.Contains(out, "initial commit") {
		t.Errorf("git log output = %q, want it to contain %q", out, "initial commit")
	}
}

// TestBashRejectsGitOutsideWorkspaceTarget confirms that the PR C-1 redirection
// check still blocks git output redirected to a path outside the workspace.
func TestBashRejectsGitOutsideWorkspaceTarget(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH; skipping allowlist integration test")
	}
	tool := newBash(testWorkspace{t.TempDir()})
	args, _ := json.Marshal(map[string]any{
		"command": "git log > /tmp/escape",
	})
	_, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatal("expected redirection escape rejection for git log > /tmp/escape")
	}
	if !strings.Contains(err.Error(), "redirection target") {
		t.Errorf("expected redirection error, got %v", err)
	}
}
