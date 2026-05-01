package llm

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func writeSkillDir(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return dir
}

func cleanupSkillTemp(t *testing.T, skill *Skill) {
	t.Helper()
	if skill == nil || skill.TempDir() == "" {
		return
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(skill.TempDir())
	})
}

// stubClient returns a non-nil *Client suitable for tests that only need to
// verify the Skill carries a client reference.
func stubClient() *Client { return &Client{} }

func TestNew_WithDir_ParsesYAMLFrontmatter(t *testing.T) {
	const body = "This is the skill instruction body.\n\nIt may span multiple lines.\n"
	content := "---\n" +
		"name: test-skill\n" +
		"description: A skill used in tests.\n" +
		"license: MIT\n" +
		"---\n" +
		body

	skill, err := NewSkill(writeSkillDir(t, content), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if skill.Client() != nil {
		t.Errorf("Client() = %p, want nil before Bind", skill.Client())
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

func TestNew_WithDir_OmitsOptionalLicense(t *testing.T) {
	content := "---\n" +
		"name: minimal\n" +
		"description: Minimal skill.\n" +
		"---\n" +
		"body\n"

	skill, err := NewSkill(writeSkillDir(t, content), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if skill.License != "" {
		t.Errorf("License = %q, want empty", skill.License)
	}
	if !strings.HasSuffix(skill.Instruction, "body\n") {
		t.Errorf("Instruction = %q, want suffix %q", skill.Instruction, "body\n")
	}
}

func TestNew_WithDir_AllowsOptionalSubdirectories(t *testing.T) {
	dir := writeSkillDir(t, "---\nname: with-subdirs\ndescription: d\n---\nbody")
	for _, sub := range []string{"scripts", "references", "examples"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	skill, err := NewSkill(dir, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if skill.Name != "with-subdirs" {
		t.Errorf("Name = %q, want %q", skill.Name, "with-subdirs")
	}
}

func TestNew_WithDir_ErrorWhenPathIsFile(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := NewSkill(file, nil, nil); err == nil {
		t.Fatal("expected error for non-directory path, got nil")
	}
}

func TestNew_WithDir_ErrorWhenSkillFileMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := NewSkill(dir, nil, nil)
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

func TestNew_WithDir_ErrorOnInvalidFrontmatter(t *testing.T) {
	content := "---\nname: [unterminated\n---\nbody\n"
	_, err := NewSkill(writeSkillDir(t, content), nil, nil)
	if err == nil {
		t.Fatal("expected error for malformed frontmatter, got nil")
	}
}

func TestNew_WithDir_CarriesBashAllowAndBlock(t *testing.T) {
	dir := writeSkillDir(t, "---\nname: x\ndescription: d\n---\n")
	allow := []string{"rg", "fd"}
	block := []string{"curl", "wget"}
	sk, err := NewSkill(dir, allow, block)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := sk.BashAllow(); !equalStrings(got, allow) {
		t.Errorf("BashAllow() = %v, want %v", got, allow)
	}
	if got := sk.BashBlock(); !equalStrings(got, block) {
		t.Errorf("BashBlock() = %v, want %v", got, block)
	}
	// Returned slices must be independent copies so callers cannot mutate
	// the Skill's internal state.
	sk.BashAllow()[0] = "mutated"
	if sk.BashAllow()[0] != "rg" {
		t.Errorf("BashAllow() returned a shared slice")
	}
}

func TestNew_WithDir_ParsesMetadataWithoutClient(t *testing.T) {
	const body = "This is a lightweight skill loading test.\n"
	content := "---\n" +
		"name: lightweight-skill\n" +
		"description: A skill used to test Load.\n" +
		"license: Apache-2.0\n" +
		"---\n" +
		body

	dir := writeSkillDir(t, content)
	skill, err := NewSkill(dir, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if skill.Client() != nil {
		t.Errorf("Client() = %p, want nil before Bind", skill.Client())
	}
	if skill.Name != "lightweight-skill" {
		t.Errorf("Name = %q, want %q", skill.Name, "lightweight-skill")
	}
	if skill.Description != "A skill used to test Load." {
		t.Errorf("Description = %q, want %q", skill.Description, "A skill used to test Load.")
	}
	if skill.License != "Apache-2.0" {
		t.Errorf("License = %q, want %q", skill.License, "Apache-2.0")
	}
	if skill.Instruction != body {
		t.Errorf("Instruction = %q, want %q", skill.Instruction, body)
	}
	if skill.Dir() == nil {
		t.Fatal("Dir() = nil, want skill filesystem")
	}
	if data, err := fs.ReadFile(skill.Dir(), SkillFile); err != nil || string(data) != content {
		t.Fatalf("Dir().ReadFile(%s) = %q, %v", SkillFile, string(data), err)
	}
	if skill.Conversation() != nil {
		t.Errorf("Conversation() should be nil before Bind")
	}
}

func TestNew_WithFS_ParsesMetadata(t *testing.T) {
	fsys := fstest.MapFS{
		"SKILL.md": &fstest.MapFile{Data: []byte("---\nname: embedded\ndescription: Embedded skill.\n---\nembedded body\n")},
	}
	skill, err := NewSkill(fsys, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, skill)
	if skill.Name != "embedded" {
		t.Fatalf("Name = %q, want %q", skill.Name, "embedded")
	}
	if skill.Description != "Embedded skill." {
		t.Fatalf("Description = %q, want %q", skill.Description, "Embedded skill.")
	}
	if skill.Instruction != "embedded body\n" {
		t.Fatalf("Instruction = %q, want %q", skill.Instruction, "embedded body\n")
	}
	if skill.Dir() == nil {
		t.Fatal("Dir() = nil, want embedded skill filesystem")
	}
	if skill.Client() != nil {
		t.Fatalf("Client() = %p, want nil", skill.Client())
	}
	if skill.Conversation() != nil {
		t.Fatal("Conversation() should be nil after New()")
	}
}

func TestNew_WithDir_AssignsUniqueTempDirs(t *testing.T) {
	content := "---\nname: x\ndescription: d\n---\nbody\n"
	first, err := NewSkill(writeSkillDir(t, content), nil, nil)
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	cleanupSkillTemp(t, first)
	second, err := NewSkill(writeSkillDir(t, content), nil, nil)
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	cleanupSkillTemp(t, second)

	type customSkillDir string
	loadedFromAlias, err := NewSkill(customSkillDir(writeSkillDir(t, content)), nil, nil)
	if err != nil {
		t.Fatalf("alias New: %v", err)
	}
	cleanupSkillTemp(t, loadedFromAlias)
	if loadedFromAlias.WorkspaceRoot() == "" {
		t.Fatal("WorkspaceRoot() = empty for string-like SkillDir")
	}

	if first.TempDir() == "" || second.TempDir() == "" {
		t.Fatalf("TempDir() should not be empty: first=%q second=%q", first.TempDir(), second.TempDir())
	}
	if first.TempDir() == second.TempDir() {
		t.Fatalf("TempDir() = %q for two skills, want unique temp dirs", first.TempDir())
	}
	for _, dir := range []string{first.TempDir(), second.TempDir()} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat temp dir %q: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("temp path %q is not a directory", dir)
		}
	}
}

func TestSkillCopiesPreserveTempDir(t *testing.T) {
	loaded, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, loaded)

	withTools, err := loaded.WithTools()
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	if withTools.TempDir() != loaded.TempDir() {
		t.Fatalf("WithTools TempDir() = %q, want %q", withTools.TempDir(), loaded.TempDir())
	}

	bound, err := loaded.Bind(stubClient(), nil, nil)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if bound.TempDir() != loaded.TempDir() {
		t.Fatalf("Bind TempDir() = %q, want %q", bound.TempDir(), loaded.TempDir())
	}
}

func TestWithAccessibleDirsOverwritesAndClearsRuntimeInstruction(t *testing.T) {
	loaded, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, loaded)
	firstDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(firstDir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested extra dir: %v", err)
	}
	if err := loaded.WithAccessibleDirs(firstDir); err != nil {
		t.Fatalf("WithAccessibleDirs: %v", err)
	}
	gotDirs := loaded.AccessibleDirs()
	if len(gotDirs) != 1 {
		t.Fatalf("AccessibleDirs() = %v, want one dir", gotDirs)
	}
	realFirstDir, err := filepath.EvalSymlinks(firstDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(firstDir): %v", err)
	}
	if gotDirs[0] != realFirstDir {
		t.Fatalf("AccessibleDirs()[0] = %q, want %q", gotDirs[0], realFirstDir)
	}

	secondDir := t.TempDir()
	if err := loaded.WithAccessibleDirs(secondDir); err != nil {
		t.Fatalf("WithAccessibleDirs overwrite: %v", err)
	}
	realSecondDir, err := filepath.EvalSymlinks(secondDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(secondDir): %v", err)
	}
	gotDirs = loaded.AccessibleDirs()
	if len(gotDirs) != 1 || gotDirs[0] != realSecondDir {
		t.Fatalf("AccessibleDirs() after overwrite = %v, want [%s]", gotDirs, realSecondDir)
	}

	bound, err := loaded.Bind(stubClient(), nil, nil)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	instructions := bound.Conversation().Instructions()
	for _, want := range []string{
		"body",
		"Workspace access:",
		loaded.TempDir(),
		realSecondDir,
		"Relative file paths and shell commands run here",
		"User-added directories",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("runtime instructions missing %q:\n%s", want, instructions)
		}
	}
	if strings.Contains(instructions, realFirstDir) {
		t.Fatalf("runtime instructions still contain overwritten dir %q:\n%s", realFirstDir, instructions)
	}

	cleared, err := loaded.WithTools()
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	if err := cleared.WithAccessibleDirs(); err != nil {
		t.Fatalf("WithAccessibleDirs clear: %v", err)
	}
	if got := cleared.AccessibleDirs(); len(got) != 0 {
		t.Fatalf("AccessibleDirs() after clear = %v, want none", got)
	}
}

func TestWithAccessibleDirsRejectsBoundSkill(t *testing.T) {
	loaded, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, loaded)
	bound, err := loaded.Bind(stubClient(), nil, nil)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if err := bound.WithAccessibleDirs(t.TempDir()); err == nil {
		t.Fatal("expected WithAccessibleDirs to reject a bound skill")
	}
}

func TestWithAccessibleDirsAndBuiltinToolsPreservesCustomTools(t *testing.T) {
	loaded, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, loaded)
	custom := NewFuncTool("custom", "custom tool", map[string]any{"type": "object"}, nil)
	withCustom, err := loaded.WithTools(custom)
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	extraDir := t.TempDir()
	withDirs, err := withCustom.WithAccessibleDirsAndBuiltinTools(extraDir)
	if err != nil {
		t.Fatalf("WithAccessibleDirsAndBuiltinTools: %v", err)
	}
	realExtraDir, err := filepath.EvalSymlinks(extraDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(extraDir): %v", err)
	}
	if got := withDirs.AccessibleDirs(); len(got) != 1 || got[0] != realExtraDir {
		t.Fatalf("AccessibleDirs() = %v, want [%s]", got, realExtraDir)
	}
	seen := make(map[string]bool)
	for _, tool := range withDirs.tools {
		seen[tool.Name()] = true
	}
	for _, name := range []string{"custom", "bash", "read_file", "pwd"} {
		if !seen[name] {
			t.Fatalf("tool %q missing from rebuilt skill tools: %v", name, seen)
		}
	}
	if got := loaded.AccessibleDirs(); len(got) != 0 {
		t.Fatalf("source AccessibleDirs() = %v, want none", got)
	}
}

func TestWithToolsCopyDoesNotShareAccessibleDirs(t *testing.T) {
	loaded, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cleanupSkillTemp(t, loaded)
	firstDir := t.TempDir()
	if err := loaded.WithAccessibleDirs(firstDir); err != nil {
		t.Fatalf("loaded.WithAccessibleDirs: %v", err)
	}
	copyWithTools, err := loaded.WithTools()
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	secondDir := t.TempDir()
	if err := copyWithTools.WithAccessibleDirs(secondDir); err != nil {
		t.Fatalf("copyWithTools.WithAccessibleDirs: %v", err)
	}
	realFirstDir, err := filepath.EvalSymlinks(firstDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(firstDir): %v", err)
	}
	realSecondDir, err := filepath.EvalSymlinks(secondDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(secondDir): %v", err)
	}
	if got := loaded.AccessibleDirs(); len(got) != 1 || got[0] != realFirstDir {
		t.Fatalf("loaded.AccessibleDirs() = %v, want [%s]", got, realFirstDir)
	}
	if got := copyWithTools.AccessibleDirs(); len(got) != 1 || got[0] != realSecondDir {
		t.Fatalf("copyWithTools.AccessibleDirs() = %v, want [%s]", got, realSecondDir)
	}
}

func TestBind_BuildsRunnableCopyFromLoadedSkill(t *testing.T) {
	dir := writeSkillDir(t, "---\nname: loaded\ndescription: d\n---\nbody\n")
	loaded, err := NewSkill(dir, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client := stubClient()
	bound, err := loaded.Bind(client, nil, nil)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if bound == loaded {
		t.Fatal("Bind returned the original skill pointer, want a fresh copy")
	}
	if bound.Client() != client {
		t.Fatalf("Client() = %p, want %p", bound.Client(), client)
	}
	if bound.Conversation() == nil {
		t.Fatal("Conversation() = nil, want a runnable conversation")
	}
	if loaded.Client() != nil {
		t.Fatalf("loaded Client() = %p, want nil after Bind", loaded.Client())
	}
	if loaded.Conversation() != nil {
		t.Fatal("loaded Conversation() changed after Bind")
	}
	if bound.Name != loaded.Name || bound.Description != loaded.Description || bound.Dir() != loaded.Dir() {
		t.Fatalf("bound skill metadata changed unexpectedly: got %+v want name=%q description=%q", bound, loaded.Name, loaded.Description)
	}
}

func TestBind_UsesSkillDirEnvFileWhenClientNil(t *testing.T) {
	dir := writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("OPENAI_API_KEY=local-key\nMODEL=local-model\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	unsetEnvForTest(t, "OPENAI_API_KEY", "OPENROUTER_API_KEY", "GEMINI_API_KEY", "MODEL", "REASONING_EFFORT", "REASONING_REPLAY")
	loaded, err := NewSkill(dir, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bound, err := loaded.Bind(nil, nil, nil)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if got := bound.Client().Model(); got != "local-model" {
		t.Fatalf("Client().Model() = %q, want %q", got, "local-model")
	}
}

func TestBind_ExplicitMissingSessionReturnsError(t *testing.T) {
	store, err := NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	loaded, err := NewSkill(writeSkillDir(t, "---\nname: x\ndescription: d\n---\nbody\n"), nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sessionID := "missing-session"
	_, err = loaded.Bind(stubClient(), nil, &sessionID, store)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Bind error = %v, want ErrSessionNotFound", err)
	}
}

func unsetEnvForTest(t *testing.T, names ...string) {
	t.Helper()
	type prior struct {
		name string
		val  string
		ok   bool
	}
	priorValues := make([]prior, 0, len(names))
	for _, name := range names {
		val, ok := os.LookupEnv(name)
		priorValues = append(priorValues, prior{name: name, val: val, ok: ok})
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
	t.Cleanup(func() {
		for _, p := range priorValues {
			if p.ok {
				_ = os.Setenv(p.name, p.val)
			} else {
				_ = os.Unsetenv(p.name)
			}
		}
	})
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
