package runner

import (
	"fmt"
	"path/filepath"
)

// AgentRuntimePaths is the runner-owned workspace context passed into an
// agent binding for one node runtime.
type AgentRuntimePaths struct {
	RunnerID       string
	NodeID         string
	SkillName      string
	MemoDir        string
	UpstreamDir    string
	DownstreamDir  string
	ScratchDir     string
	ExchangeDir    string
	ArchiveMemoDir string
	AccessibleDirs []string
}

// AgentRuntimePaths returns the typed runner workspace context for one node.
func (w *Workspace) AgentRuntimePaths(nodeID string, skillName string, accessibleDirs []string) (AgentRuntimePaths, error) {
	if w == nil {
		return AgentRuntimePaths{}, fmt.Errorf("runner: workspace is required")
	}
	sk, ok := w.Skill(nodeID)
	if !ok {
		return AgentRuntimePaths{}, fmt.Errorf("runner: unknown workspace skill %q", nodeID)
	}
	dirs := accessibleDirs
	if len(dirs) == 0 {
		dirs = sk.accessibleDirs
	}
	return AgentRuntimePaths{
		RunnerID:       w.runnerID,
		NodeID:         nodeID,
		SkillName:      skillName,
		MemoDir:        sk.MemoDir,
		UpstreamDir:    sk.UpstreamDir,
		DownstreamDir:  sk.DownstreamDir,
		ScratchDir:     sk.ScratchDir,
		ExchangeDir:    w.ExchangeDir(),
		ArchiveMemoDir: filepath.Join(w.ArchiveDir(), "memo", nodeID),
		AccessibleDirs: append([]string(nil), dirs...),
	}, nil
}
