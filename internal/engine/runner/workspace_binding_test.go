package runner

import (
	"strings"
	"testing"
)

func TestNodeRuntimePathsWithValidWorkspace(t *testing.T) {
	r := newTestRunner()
	workspace, err := ProvisionWorkspace(t.TempDir(), r.ID(), []string{"node-1"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	r.workspace = workspace

	paths, err := r.nodeRuntimePaths("node-1", "skill-one", nil)
	if err != nil {
		t.Fatalf("nodeRuntimePaths: %v", err)
	}

	skillWS, ok := workspace.Skill("node-1")
	if !ok {
		t.Fatal("workspace.Skill(node-1) = false")
	}
	if paths.RunnerID != r.ID() || paths.NodeID != "node-1" || paths.SkillName != "skill-one" {
		t.Fatalf("runtime paths = %+v", paths)
	}
	if paths.MemoDir != skillWS.MemoDir || paths.UpstreamDir != skillWS.UpstreamDir || paths.DownstreamDir != skillWS.DownstreamDir || paths.ScratchDir != skillWS.ScratchDir {
		t.Fatalf("runtime dirs = %+v, want workspace dirs %+v", paths, skillWS)
	}
	if paths.ExchangeDir != workspace.ExchangeDir() {
		t.Fatalf("ExchangeDir = %q, want %q", paths.ExchangeDir, workspace.ExchangeDir())
	}
}

func TestNodeRuntimePathsErrorOnMissingSkill(t *testing.T) {
	r := newTestRunner()
	workspace, err := ProvisionWorkspace(t.TempDir(), r.ID(), []string{"node-1"}, nil)
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
