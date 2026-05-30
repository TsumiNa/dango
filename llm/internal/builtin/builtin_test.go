package builtin

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestToolsReturnsExpectedNames(t *testing.T) {
	root := t.TempDir()
	tools, err := Tools(testWorkspace{root}, ToolSetConfig{})
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	want := []string{"bash", "read_file", "write_file", "edit_file", "delete_file", "move_file", "grep", "pipeline_search_replace", "file_excerpt", "artifact_catalog", "structured_preview"}
	if got := toolNames(tools); !slices.Equal(got, want) {
		t.Fatalf("Tools names = %v, want %v", got, want)
	}
}

func TestToolsAppendsExtras(t *testing.T) {
	root := t.TempDir()
	tools, err := Tools(testWorkspace{root}, ToolSetConfig{Extras: []ExtraTool{ExtraListDir, ExtraPwd}})
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	want := []string{"bash", "read_file", "write_file", "edit_file", "delete_file", "move_file", "grep", "pipeline_search_replace", "file_excerpt", "artifact_catalog", "structured_preview", "list_dir", "pwd"}
	if got := toolNames(tools); !slices.Equal(got, want) {
		t.Fatalf("Tools names = %v, want %v", got, want)
	}
}

func TestToolsRejectsUnknownExtra(t *testing.T) {
	root := t.TempDir()
	_, err := Tools(testWorkspace{root}, ToolSetConfig{Extras: []ExtraTool{ExtraTool("nope")}})
	if err == nil {
		t.Fatal("expected unknown extra error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %v, want tool name", err)
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
	tools, err := Tools(testWorkspace{root}, ToolSetConfig{BashAllow: []string{"helper-bin"}, BashBlock: []string{"curl"}})
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
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

func toolNames(tools []tool) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name()
	}
	return names
}
