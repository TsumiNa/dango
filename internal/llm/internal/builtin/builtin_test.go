package builtin

import (
	"context"
	"encoding/json"
	"testing"
)

func TestToolsReturnsExpectedNames(t *testing.T) {
	root := t.TempDir()
	got := map[string]bool{}
	for _, tool := range Tools(testWorkspace{root}, nil, nil) {
		got[tool.Name()] = true
	}
	for _, want := range []string{"bash", "read_file", "write_file", "edit_file", "delete_file", "move_file", "list_dir", "grep", "pwd"} {
		if !got[want] {
			t.Errorf("Tools missing %q", want)
		}
	}
}

func TestBashForwardsAllowlistOption(t *testing.T) {
	root := t.TempDir()
	bash := newBash(testWorkspace{root}, withAllowlist([]string{"echo"}))
	// rm is not in the custom allowlist.
	args, _ := json.Marshal(map[string]any{"command": "rm -rf /"})
	if _, err := bash.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected allowlist rejection for rm")
	}
}

func TestWithAllowlistAdjust(t *testing.T) {
	root := t.TempDir()
	// Block curl (default-allowed) and allow a bespoke command.
	tools := Tools(testWorkspace{root}, []string{"helper-bin"}, []string{"curl"})
	var bash tool
	for _, tool := range tools {
		if tool.Name() == "bash" {
			bash = tool
		}
	}
	if bash == nil {
		t.Fatal("bash tool not returned by Tools")
	}

	// Blocked command must be rejected even though it is in defaultAllowlist.
	args, _ := json.Marshal(map[string]any{"command": "curl https://example.com"})
	if _, err := bash.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected blocked curl to be rejected")
	}

	// A default-allowed command that was not blocked must still be permitted
	// (resolved via the bash tool's configured allowlist, independent of
	// whether the binary exists on the host).
	cfg := newConfig([]option{withAllowlistAdjust([]string{"helper-bin"}, []string{"curl"})})
	set := cfg.resolveAllowlist()
	if _, ok := set["echo"]; !ok {
		t.Error("echo should remain in adjusted allowlist")
	}
	if _, ok := set["helper-bin"]; !ok {
		t.Error("helper-bin should have been added by adjust")
	}
	if _, ok := set["curl"]; ok {
		t.Error("curl should have been removed by adjust")
	}
}

func TestWithAllowlistAdjust_BlockWinsOverAllow(t *testing.T) {
	cfg := newConfig([]option{withAllowlistAdjust([]string{"foo"}, []string{"foo"})})
	if _, ok := cfg.resolveAllowlist()["foo"]; ok {
		t.Error("block should override allow for the same entry")
	}
}
