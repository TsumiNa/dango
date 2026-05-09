package runner

import (
	"os"
	"path/filepath"
	"testing"

	persistencepkg "github.com/tsumina/dango/internal/engine/runner/persistence"
)

func TestProvisionWorkspaceCreatesLayoutAndAccessibleDirs(t *testing.T) {
	globalRoot := t.TempDir()
	workspace, err := ProvisionWorkspace(globalRoot, "runner-1", []string{"alpha", "beta"}, persistencepkg.DefaultPathRule)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}

	expectedRoot := filepath.Join(globalRoot, "task_runner-1")
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
	if !containsPath(alphaDirs, alpha.MemoDir) || !containsPath(alphaDirs, alpha.InboxDir) || !containsPath(alphaDirs, alpha.OutboxDir) || !containsPath(alphaDirs, alpha.WorkingDir) || !containsPath(alphaDirs, workspace.ExchangeDir()) {
		t.Fatalf("alpha accessible dirs = %v, want own dirs + exchange", alphaDirs)
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

func TestRouteOutboxToInboxCopiesHandoffAndArtifacts(t *testing.T) {
	workspace, err := ProvisionWorkspace(t.TempDir(), "runner-1", []string{"upstream", "downstream"}, persistencepkg.DefaultPathRule)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	upstream, ok := workspace.Skill("upstream")
	if !ok {
		t.Fatal("upstream skill workspace missing")
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

	if err := workspace.RouteOutboxToInbox("upstream", "downstream"); err != nil {
		t.Fatalf("RouteOutboxToInbox: %v", err)
	}
	deliveredHandoff := filepath.Join(workspace.Root(), "skills", "downstream", "inbox", "upstream", "handoff.md")
	deliveredArtifact := filepath.Join(workspace.Root(), "skills", "downstream", "inbox", "upstream", "artifacts", "data", "sample.csv")
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
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}
