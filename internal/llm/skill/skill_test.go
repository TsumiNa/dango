package skill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsumina/dango/internal/llm"
)

func writeSkillDir(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return dir
}

// stubClient returns a non-nil *Client suitable for tests that only need to
// verify the Skill carries a client reference.
func stubClient() *llm.Client { return &llm.Client{} }

func TestNewSkillFromDir_ParsesYAMLFrontmatter(t *testing.T) {
	const body = "This is the skill instruction body.\n\nIt may span multiple lines.\n"
	content := "---\n" +
		"name: test-skill\n" +
		"description: A skill used in tests.\n" +
		"license: MIT\n" +
		"---\n" +
		body

	dir := writeSkillDir(t, content)
	client := stubClient()
	skill, err := NewSkillFromDir(dir, client)
	if err != nil {
		t.Fatalf("NewSkillFromDir: %v", err)
	}

	if skill.Client() != client {
		t.Errorf("Client() = %p, want %p", skill.Client(), client)
	}
	if skill.Name != "test-skill" {
		t.Errorf("Name = %q, want %q", skill.Name, "test-skill")
	}
	if skill.Description != "A skill used in tests." {
		t.Errorf("Description = %q, want %q", skill.Description, "A skill used in tests.")
	}
	if skill.License != "MIT" {
		t.Errorf("License = %q, want %q", skill.License, "MIT")
	}
	if skill.Instruction != body {
		t.Errorf("Instruction = %q, want %q", skill.Instruction, body)
	}
}

func TestNewSkillFromDir_OmitsOptionalLicense(t *testing.T) {
	content := "---\n" +
		"name: minimal\n" +
		"description: Minimal skill.\n" +
		"---\n" +
		"body\n"

	skill, err := NewSkillFromDir(writeSkillDir(t, content), stubClient())
	if err != nil {
		t.Fatalf("NewSkillFromDir: %v", err)
	}
	if skill.License != "" {
		t.Errorf("License = %q, want empty", skill.License)
	}
	if !strings.HasSuffix(skill.Instruction, "body\n") {
		t.Errorf("Instruction = %q, want suffix %q", skill.Instruction, "body\n")
	}
}

func TestNewSkillFromDir_AllowsOptionalSubdirectories(t *testing.T) {
	dir := writeSkillDir(t, "---\nname: with-subdirs\ndescription: d\n---\nbody")
	for _, sub := range []string{"scripts", "references", "examples"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	skill, err := NewSkillFromDir(dir, stubClient())
	if err != nil {
		t.Fatalf("NewSkillFromDir: %v", err)
	}
	if skill.Name != "with-subdirs" {
		t.Errorf("Name = %q, want %q", skill.Name, "with-subdirs")
	}
}

func TestNewSkillFromDir_ErrorWhenPathIsFile(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := NewSkillFromDir(file, stubClient()); err == nil {
		t.Fatal("expected error for non-directory path, got nil")
	}
}

func TestNewSkillFromDir_ErrorWhenSkillFileMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := NewSkillFromDir(dir, stubClient())
	if err == nil {
		t.Fatal("expected error when SKILL.md is missing, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want errors.Is(err, os.ErrNotExist)", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, SkillFile) || !strings.Contains(msg, dir) {
		t.Errorf("err message %q should mention %q and %q", msg, SkillFile, dir)
	}
}

func TestNewSkillFromDir_ErrorOnInvalidFrontmatter(t *testing.T) {
	content := "---\nname: [unterminated\n---\nbody\n"
	_, err := NewSkillFromDir(writeSkillDir(t, content), stubClient())
	if err == nil {
		t.Fatal("expected error for malformed frontmatter, got nil")
	}
}

func TestNewSkillFromDir_ErrorWhenClientNil(t *testing.T) {
	dir := writeSkillDir(t, "---\nname: x\ndescription: d\n---\n")
	if _, err := NewSkillFromDir(dir, nil); err == nil {
		t.Fatal("expected error when client is nil, got nil")
	}
}
