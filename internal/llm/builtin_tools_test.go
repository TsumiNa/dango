package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinToolsReturnsDefaultToolSet(t *testing.T) {
	skill, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), DefaultSkillConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, skill)
	tools, err := skill.BuiltinTools()
	if err != nil {
		t.Fatalf("BuiltinTools: %v", err)
	}
	want := []string{"bash", "read_file", "write_file", "edit_file", "delete_file", "move_file", "grep", "pipeline_search_replace"}
	if got := llmToolNames(tools); !slices.Equal(got, want) {
		t.Fatalf("BuiltinTools names = %v, want %v", got, want)
	}
}

func TestBuiltinToolsRespectsBuiltinExtras(t *testing.T) {
	skill, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), SkillConfig{BuiltinExtras: []string{"list_dir", "pwd"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, skill)
	tools, err := skill.BuiltinTools()
	if err != nil {
		t.Fatalf("BuiltinTools: %v", err)
	}
	want := []string{"bash", "read_file", "write_file", "edit_file", "delete_file", "move_file", "grep", "pipeline_search_replace", "list_dir", "pwd"}
	if got := llmToolNames(tools); !slices.Equal(got, want) {
		t.Fatalf("BuiltinTools names = %v, want %v", got, want)
	}
}

func TestBuiltinToolsWrapsUnknownBuiltinExtra(t *testing.T) {
	skill, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), SkillConfig{BuiltinExtras: []string{"nope"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, skill)
	_, err = skill.BuiltinTools()
	if err == nil {
		t.Fatal("expected BuiltinTools error")
	}
	if !strings.Contains(err.Error(), "skill: configure built-in tools") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("BuiltinTools error = %v, want skill context and extra name", err)
	}
}

func TestBuiltinToolsAppliesBashAllowAndBlock(t *testing.T) {
	skill, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), SkillConfig{BashAllow: []string{"helper-bin"}, BashBlock: []string{"curl"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, skill)
	tools, err := skill.BuiltinTools()
	if err != nil {
		t.Fatalf("BuiltinTools: %v", err)
	}
	var bash Tool
	for _, tool := range tools {
		if tool.Name() == "bash" {
			bash = tool
		}
	}
	if bash == nil {
		t.Fatal("BuiltinTools did not return bash")
	}

	args, _ := json.Marshal(map[string]any{"command": "curl https://example.com"})
	if _, err := bash.Execute(context.Background(), string(args)); err == nil {
		t.Fatal("expected blocked curl to be rejected")
	}
}

func TestBuiltinToolsResolveRelativePathsInSkillTempDir(t *testing.T) {
	skill, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), DefaultSkillConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, skill)
	tools, err := skill.BuiltinTools()
	if err != nil {
		t.Fatalf("BuiltinTools: %v", err)
	}
	writeFile := findTool(t, tools, "write_file")

	args, _ := json.Marshal(map[string]any{"path": "notes/out.txt", "content": "hello temp"})
	if _, err := writeFile.Execute(context.Background(), string(args)); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(skill.TempDir(), "notes/out.txt"))
	if err != nil {
		t.Fatalf("read temp output: %v", err)
	}
	if string(data) != "hello temp" {
		t.Fatalf("temp output = %q, want %q", string(data), "hello temp")
	}
	if _, err := os.Stat(filepath.Join(skill.WorkspaceRoot(), "notes/out.txt")); !os.IsNotExist(err) {
		t.Fatalf("relative write should not create source workspace file, stat err = %v", err)
	}
}

func TestBuiltinToolsAllowAbsoluteSkillRootAndRejectOutside(t *testing.T) {
	skill, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), DefaultSkillConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, skill)
	sourceFile := filepath.Join(skill.WorkspaceRoot(), "reference.txt")
	if err := os.WriteFile(sourceFile, []byte("from source"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	tools, err := skill.BuiltinTools()
	if err != nil {
		t.Fatalf("BuiltinTools: %v", err)
	}
	readFile := findTool(t, tools, "read_file")
	writeFile := findTool(t, tools, "write_file")

	readArgs, _ := json.Marshal(map[string]any{"path": sourceFile})
	out, err := readFile.Execute(context.Background(), string(readArgs))
	if err != nil {
		t.Fatalf("read source file: %v", err)
	}
	if out != "from source" {
		t.Fatalf("read source file = %q, want %q", out, "from source")
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeArgs, _ := json.Marshal(map[string]any{"path": outside, "content": "escape"})
	if _, err := writeFile.Execute(context.Background(), string(writeArgs)); err == nil {
		t.Fatal("expected write outside temp/source roots to be rejected")
	}
}

func TestBuiltinToolsAllowAccessibleDirs(t *testing.T) {
	skill, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), DefaultSkillConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, skill)
	extraDir := t.TempDir()
	nestedFile := filepath.Join(extraDir, "nested", "source.txt")
	if err := os.MkdirAll(filepath.Dir(nestedFile), 0o755); err != nil {
		t.Fatalf("mkdir nested extra dir: %v", err)
	}
	if err := os.WriteFile(nestedFile, []byte("from extra"), 0o644); err != nil {
		t.Fatalf("write extra file: %v", err)
	}
	if err := skill.SetAccessibleDirs(extraDir); err != nil {
		t.Fatalf("SetAccessibleDirs: %v", err)
	}
	tools, err := skill.BuiltinTools()
	if err != nil {
		t.Fatalf("BuiltinTools: %v", err)
	}
	readFile := findTool(t, tools, "read_file")
	writeFile := findTool(t, tools, "write_file")

	readArgs, _ := json.Marshal(map[string]any{"path": nestedFile})
	out, err := readFile.Execute(context.Background(), string(readArgs))
	if err != nil {
		t.Fatalf("read accessible dir file: %v", err)
	}
	if out != "from extra" {
		t.Fatalf("read accessible dir file = %q, want %q", out, "from extra")
	}
	writePath := filepath.Join(extraDir, "nested", "result.txt")
	writeArgs, _ := json.Marshal(map[string]any{"path": writePath, "content": "generated"})
	if _, err := writeFile.Execute(context.Background(), string(writeArgs)); err != nil {
		t.Fatalf("write accessible dir file: %v", err)
	}
	data, err := os.ReadFile(writePath)
	if err != nil {
		t.Fatalf("read generated accessible dir file: %v", err)
	}
	if string(data) != "generated" {
		t.Fatalf("generated accessible dir file = %q, want %q", string(data), "generated")
	}
}

func findTool(t *testing.T, tools []Tool, name string) Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func llmToolNames(tools []Tool) []string {
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name()
	}
	return names
}
