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
	resourceDirs := exchangeResourceDirsFromOutputs(inputs, nil)
	for _, dir := range resourceDirs {
		if !containsDir(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func (r *Runner) workspaceForNode(nodeID string) (SkillWorkspace, error) {
	if r.workspace == nil {
		return SkillWorkspace{}, fmt.Errorf("runner: workspace not configured")
	}
	workspace, ok := r.workspace.Skill(nodeID)
	if !ok {
		return SkillWorkspace{}, fmt.Errorf("runner: workspace missing for node %q", nodeID)
	}
	return workspace, nil
}
