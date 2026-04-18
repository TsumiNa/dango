package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBashRunsInRoot(t *testing.T) {
	root := t.TempDir()
	tool := NewBash(root)
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
	tool := NewBash(t.TempDir())
	if _, err := tool.Execute(context.Background(), `{"command": "  "}`); err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestBashInvalidJSON(t *testing.T) {
	tool := NewBash(t.TempDir())
	if _, err := tool.Execute(context.Background(), `not json`); err == nil {
		t.Fatal("expected error for invalid JSON arguments")
	}
}

func TestBashTimeout(t *testing.T) {
	tool := NewBash(t.TempDir())
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
	tool := NewBash(t.TempDir())
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
	tool := NewBash(t.TempDir())
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
	tool := NewBash(root)
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
	tool := NewBash(t.TempDir())
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
	tool := NewBash(root)
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

func TestBashAllowlistBlocksRm(t *testing.T) {
	tool := NewBash(t.TempDir())
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
	tool := NewBash(t.TempDir())
	// echo is allowed; mv is not. The whole command must be rejected.
	args, _ := json.Marshal(map[string]any{"command": "echo hi && mv a b"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected pipeline allowlist rejection")
	}
}

func TestBashAllowlistBlocksInSubshell(t *testing.T) {
	tool := NewBash(t.TempDir())
	args, _ := json.Marshal(map[string]any{"command": "echo $(rm /tmp/x)"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected subshell allowlist rejection")
	}
}

func TestBashAllowlistBlocksDynamicHead(t *testing.T) {
	tool := NewBash(t.TempDir())
	// Dynamic head such as $CMD cannot be statically checked; reject it.
	args, _ := json.Marshal(map[string]any{"command": "$CMD hello"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected dynamic-head rejection")
	}
}

func TestBashAllowlistAcceptsPathedExecutable(t *testing.T) {
	tool := NewBash(t.TempDir())
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
	tool := NewBash(t.TempDir(), WithAllowlist([]string{"rm"}))
	// With a custom allowlist, rm is now permitted but echo is not.
	args, _ := json.Marshal(map[string]any{"command": "echo hi"})
	if _, err := tool.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected echo to be rejected by custom allowlist")
	}
}

func TestBashAllowlistDisabled(t *testing.T) {
	tool := NewBash(t.TempDir(), WithoutAllowlist())
	// Without enforcement, any command is accepted by the allowlist check
	// (the command itself may still fail, but not due to allowlist).
	args, _ := json.Marshal(map[string]any{"command": "true"})
	if _, err := tool.Execute(context.Background(), string(args)); err != nil {
		t.Errorf("expected success with allowlist disabled, got %v", err)
	}
}

func TestBashAllowlistAllowsEnvPrefix(t *testing.T) {
	tool := NewBash(t.TempDir())
	args, _ := json.Marshal(map[string]any{"command": "FOO=bar echo ok"})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("env-prefixed echo should be allowed: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("env-prefixed echo output = %q", out)
	}
}
