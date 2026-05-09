package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	persistencepkg "github.com/tsumina/dango/internal/engine/runner/persistence"
)

func TestProvisionWorkspaceCreatesLayoutAndAccessibleDirs(t *testing.T) {
	globalRoot := t.TempDir()
	workspace, err := ProvisionWorkspace(globalRoot, "runner-1", []string{"alpha", "beta"}, persistencepkg.DefaultPathRule)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}

	canonicalRoot, err := ensureCanonicalDir(globalRoot)
	if err != nil {
		t.Fatalf("ensureCanonicalDir(globalRoot): %v", err)
	}
	expectedRoot := filepath.Join(canonicalRoot, "task_runner-1")
	if workspace.Root() != expectedRoot {
		t.Fatalf("workspace root = %q, want %q", workspace.Root(), expectedRoot)
	}
	for _, dir := range []string{
		workspace.Root(),
		workspace.ExchangeDir(),
		workspace.ArchiveDir(),
		filepath.Join(workspace.Root(), "skills", "alpha", "memo"),
		filepath.Join(workspace.Root(), "skills", "alpha", "inbox"),
		filepath.Join(workspace.Root(), "skills", "alpha", "outbox"),
		filepath.Join(workspace.Root(), "skills", "alpha", "workspace"),
		filepath.Join(workspace.Root(), "skills", "beta", "memo"),
		filepath.Join(workspace.Root(), "skills", "beta", "inbox"),
		filepath.Join(workspace.Root(), "skills", "beta", "outbox"),
		filepath.Join(workspace.Root(), "skills", "beta", "workspace"),
	} {
		info, statErr := os.Stat(dir)
		if statErr != nil {
			t.Fatalf("stat %q: %v", dir, statErr)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a directory", dir)
		}
	}

	alpha, ok := workspace.Skill("alpha")
	if !ok {
		t.Fatal("alpha skill workspace missing")
	}
	beta, ok := workspace.Skill("beta")
	if !ok {
		t.Fatal("beta skill workspace missing")
	}
	alphaDirs, err := workspace.AccessibleDirs("alpha")
	if err != nil {
		t.Fatalf("AccessibleDirs(alpha): %v", err)
	}
	for _, wantPath := range []string{alpha.MemoDir, alpha.InboxDir, alpha.OutboxDir, alpha.WorkingDir, workspace.ExchangeDir()} {
		if !containsPath(alphaDirs, wantPath) {
			t.Fatalf("alpha accessible dirs = %v, missing %q", alphaDirs, wantPath)
		}
	}
	if containsPath(alphaDirs, beta.MemoDir) || containsPath(alphaDirs, beta.OutboxDir) || containsPath(alphaDirs, beta.WorkingDir) {
		t.Fatalf("alpha accessible dirs leaked beta private paths: %v", alphaDirs)
	}
}

func TestProvisionWorkspaceRejectsInvalidRuleOutputs(t *testing.T) {
	cases := []struct {
		name string
		rule persistencepkg.PathRule
	}{
		{
			name: "empty path",
			rule: func(string) string { return "" },
		},
		{
			name: "absolute path",
			rule: func(string) string { return "/tmp/escape" },
		},
		{
			name: "parent escaping path",
			rule: func(string) string { return "../escape" },
		},
		{
			name: "multi segment path",
			rule: func(string) string { return "foo/bar" },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ProvisionWorkspace(t.TempDir(), "runner-1", []string{"node"}, tc.rule)
			if err == nil {
				t.Fatal("ProvisionWorkspace returned nil error for invalid path rule output")
			}
		})
	}
}

func TestHandoffRejectsSymlinkSourceArtifact(t *testing.T) {
	workspace, err := ProvisionWorkspace(t.TempDir(), "runner-1", []string{"upstream", "downstream"}, persistencepkg.DefaultPathRule)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	upstream, ok := workspace.Skill("upstream")
	if !ok {
		t.Fatal("upstream skill workspace missing")
	}
	if _, ok := workspace.Skill("downstream"); !ok {
		t.Fatal("downstream skill workspace missing")
	}

	artifactDir := filepath.Join(upstream.OutboxDir, "artifacts", "data")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(artifact dir): %v", err)
	}
	artifactLink := filepath.Join(artifactDir, "outside.txt")
	if err := os.Symlink("/etc/hosts", artifactLink); err != nil {
		t.Fatalf("Symlink(artifact): %v", err)
	}

	err = workspace.Handoff("upstream", "downstream")
	if err == nil {
		t.Fatal("Handoff returned nil error for symbolic-link artifact source")
	}
	if !strings.Contains(err.Error(), "must not be symbolic link") {
		t.Fatalf("Handoff error = %v, want symbolic-link rejection", err)
	}
}

func TestProvisionWorkspaceRejectsPathCollision(t *testing.T) {
	root := t.TempDir()
	collisionPath := filepath.Join(root, "task_runner-1")
	if err := os.MkdirAll(collisionPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(collisionPath): %v", err)
	}
	_, err := ProvisionWorkspace(root, "runner-1", []string{"node"}, persistencepkg.DefaultPathRule)
	if err == nil {
		t.Fatal("ProvisionWorkspace returned nil error for path collision")
	}
}

func TestHandoffSymlinksHandoffAndArtifacts(t *testing.T) {
	workspace, err := ProvisionWorkspace(t.TempDir(), "runner-1", []string{"upstream", "downstream"}, persistencepkg.DefaultPathRule)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	upstream, ok := workspace.Skill("upstream")
	if !ok {
		t.Fatal("upstream skill workspace missing")
	}
	downstream, ok := workspace.Skill("downstream")
	if !ok {
		t.Fatal("downstream skill workspace missing")
	}
	handoffPath := filepath.Join(upstream.OutboxDir, "handoff.md")
	if err := os.WriteFile(handoffPath, []byte("# handoff\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(handoff): %v", err)
	}
	artifactPath := filepath.Join(upstream.OutboxDir, "artifacts", "data", "sample.csv")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(artifact parent): %v", err)
	}
	if err := os.WriteFile(artifactPath, []byte("x\n1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(artifact): %v", err)
	}

	if err := workspace.Handoff("upstream", "downstream"); err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	deliveredHandoff := filepath.Join(downstream.InboxDir, "upstream", "handoff.md")
	deliveredArtifact := filepath.Join(downstream.InboxDir, "upstream", "artifacts", "data", "sample.csv")
	handoffContent, err := os.ReadFile(deliveredHandoff)
	if err != nil {
		t.Fatalf("ReadFile(delivered handoff): %v", err)
	}
	if string(handoffContent) != "# handoff\n" {
		t.Fatalf("delivered handoff content = %q, want %q", string(handoffContent), "# handoff\n")
	}
	artifactContent, err := os.ReadFile(deliveredArtifact)
	if err != nil {
		t.Fatalf("ReadFile(delivered artifact): %v", err)
	}
	if string(artifactContent) != "x\n1\n" {
		t.Fatalf("delivered artifact content = %q, want %q", string(artifactContent), "x\n1\n")
	}
	handoffLinkInfo, err := os.Lstat(deliveredHandoff)
	if err != nil {
		t.Fatalf("Lstat(delivered handoff): %v", err)
	}
	if handoffLinkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("delivered handoff is not a symlink")
	}
	handoffTarget, err := os.Readlink(deliveredHandoff)
	if err != nil {
		t.Fatalf("Readlink(delivered handoff): %v", err)
	}
	if handoffTarget != handoffPath {
		t.Fatalf("handoff symlink target = %q, want %q", handoffTarget, handoffPath)
	}
	artifactLinkInfo, err := os.Lstat(deliveredArtifact)
	if err != nil {
		t.Fatalf("Lstat(delivered artifact): %v", err)
	}
	if artifactLinkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("delivered artifact is not a symlink")
	}
	artifactTarget, err := os.Readlink(deliveredArtifact)
	if err != nil {
		t.Fatalf("Readlink(delivered artifact): %v", err)
	}
	if artifactTarget != artifactPath {
		t.Fatalf("artifact symlink target = %q, want %q", artifactTarget, artifactPath)
	}
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}
