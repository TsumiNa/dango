package runner

import "fmt"

func (r *Runner) provisionWorkspace(nodeIDs []string) error {
	if r == nil {
		return nil
	}
	// Empty workspaceRoot intentionally disables workspace provisioning.
	if r.workspaceRoot == "" {
		return nil
	}
	workspace, err := ProvisionWorkspace(r.workspaceRoot, r.id, nodeIDs, r.rootPathRule)
	if err != nil {
		return err
	}
	r.workspace = workspace
	return nil
}

func (r *Runner) nodeAccessibleDirs(nodeID string, inputs map[string]any) []string {
	var dirs []string
	if r.workspace != nil && nodeID != "" {
		workspaceDirs, err := r.workspace.AccessibleDirs(nodeID)
		if err == nil {
			dirs = append(dirs, workspaceDirs...)
		}
	}
	allowedRoots := r.resourceAllowedRoots()
	for _, root := range r.trustedResourceRoots {
		if !containsDir(dirs, root) {
			dirs = append(dirs, root)
		}
	}
	if len(allowedRoots) > 0 {
		resourceDirs := handoffArtifactDirsFromOutputs(inputs, allowedRoots, r.workspace)
		for _, dir := range resourceDirs {
			if !containsDir(dirs, dir) {
				dirs = append(dirs, dir)
			}
		}
	}
	return dirs
}

func (r *Runner) nodeRuntimePaths(nodeID string, skillName string, inputs map[string]any) (ExecutorRuntimePaths, error) {
	paths := ExecutorRuntimePaths{
		RunnerID:       r.id,
		NodeID:         nodeID,
		SkillName:      skillName,
		AccessibleDirs: append([]string(nil), r.nodeAccessibleDirs(nodeID, inputs)...),
	}
	if r.workspace == nil || nodeID == "" {
		return paths, nil
	}
	workspacePaths, err := r.workspace.ExecutorRuntimePaths(nodeID, skillName, paths.AccessibleDirs)
	if err != nil {
		return ExecutorRuntimePaths{}, fmt.Errorf("runner: resolve runtime paths for node %q: %w", nodeID, err)
	}
	return workspacePaths, nil
}

func (r *Runner) resourceAllowedRoots() []string {
	var roots []string
	if r.workspace != nil {
		if root, ok := canonicalExistingDir(r.workspace.Root()); ok {
			roots = append(roots, root)
		}
	}
	for _, root := range r.trustedResourceRoots {
		if !containsDir(roots, root) {
			roots = append(roots, root)
		}
	}
	return roots
}
