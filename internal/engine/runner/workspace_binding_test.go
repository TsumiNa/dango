package runner

import (
	"strings"
	"testing"
)

func TestNodeRuntimePathsReturnsTypedWorkspacePaths(t *testing.T) {
	r := newTestRunner()
	workspace, err := ProvisionWorkspace(t.TempDir(), r.ID(), []string{"only"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	r.workspace = workspace

	paths, err := r.nodeRuntimePaths("only", "skill-one", nil)
	if err != nil {
		t.Fatalf("nodeRuntimePaths: %v", err)
	}

	skillWS, ok := workspace.Skill("only")
	if !ok {
		t.Fatal("workspace.Skill(only) = false")
	}
	if paths.RunnerID != r.ID() || paths.NodeID != "only" || paths.SkillName != "skill-one" {
		t.Fatalf("runtime paths = %+v", paths)
	}
	if paths.MemoDir != skillWS.MemoDir || paths.UpstreamDir != skillWS.UpstreamDir || paths.DownstreamDir != skillWS.DownstreamDir || paths.ScratchDir != skillWS.ScratchDir {
		t.Fatalf("runtime dirs = %+v, want workspace dirs %+v", paths, skillWS)
	}
	if paths.ExchangeDir != workspace.ExchangeDir() {
		t.Fatalf("ExchangeDir = %q, want %q", paths.ExchangeDir, workspace.ExchangeDir())
	}
}

func TestNodeRuntimePathsReturnsWorkspaceResolutionError(t *testing.T) {
	r := newTestRunner()
	workspace, err := ProvisionWorkspace(t.TempDir(), r.ID(), []string{"other"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	r.workspace = workspace

	_, err = r.nodeRuntimePaths("missing", "skill-one", nil)
	if err == nil {
		t.Fatal("nodeRuntimePaths returned nil error for unknown workspace skill")
	}
	if !strings.Contains(err.Error(), `runner: resolve runtime paths for node "missing"`) {
		t.Fatalf("nodeRuntimePaths error = %v", err)
	}
}
