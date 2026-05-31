package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	runnerpkg "github.com/tsumina/dango/engine/runner"
	streampkg "github.com/tsumina/dango/stream"
)

func cloneAgentRuntimePaths(in runnerpkg.AgentRuntimePaths) runnerpkg.AgentRuntimePaths {
	out := in
	out.AccessibleDirs = append([]string(nil), in.AccessibleDirs...)
	return out
}

func (e *Agent) currentRuntimePaths() runnerpkg.AgentRuntimePaths {
	paths := cloneAgentRuntimePaths(e.runtimePaths)
	if paths.RunnerID == "" {
		paths.RunnerID = "runner"
	}
	if paths.NodeID == "" && e.planner != nil {
		paths.NodeID = e.planner.ID
	}
	if paths.NodeID == "" {
		paths.NodeID = "node"
	}
	if paths.SkillName == "" && e.skill != nil {
		paths.SkillName = e.skill.Name
	}
	return paths
}

func (e *Agent) snapshotMemos(stage string, paths runnerpkg.AgentRuntimePaths) error {
	if paths.MemoDir == "" || paths.ArchiveMemoDir == "" {
		return nil
	}
	memoRoot, err := filepath.EvalSymlinks(paths.MemoDir)
	if err != nil {
		return fmt.Errorf("orchestrate: resolve memo dir: %w", err)
	}
	memoRoot, err = filepath.Abs(memoRoot)
	if err != nil {
		return fmt.Errorf("orchestrate: resolve memo dir abs path: %w", err)
	}
	stageRoot := filepath.Join(paths.ArchiveMemoDir, stage)
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		return fmt.Errorf("orchestrate: create memo snapshot dir: %w", err)
	}
	return filepath.WalkDir(paths.MemoDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		resolvedPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		resolvedPath, err = filepath.Abs(resolvedPath)
		if err != nil {
			return err
		}
		if !pathWithinDir(memoRoot, resolvedPath) {
			return fmt.Errorf("orchestrate: memo snapshot path escapes memo dir: %s", path)
		}
		rel, err := filepath.Rel(paths.MemoDir, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		doc := runnerpkg.MemoDocument{
			ChannelHeader: streampkg.ChannelHeader{
				RunnerID:  paths.RunnerID,
				CreatedAt: time.Now(),
			},
			NodeID:    paths.NodeID,
			SkillName: paths.SkillName,
			Path:      filepath.ToSlash(filepath.Join("memo", rel)),
			Body:      string(body),
		}
		raw, err := doc.Markdown()
		if err != nil {
			return err
		}
		dst := filepath.Join(stageRoot, rel+".memo.md")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, []byte(raw), 0o644)
	})
}

func pathWithinDir(root string, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
