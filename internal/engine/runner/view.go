package runner

// RunnerView is the query-facing snapshot callers obtain via [Runner.View].
type RunnerView struct {
	RunnerID string         `json:"runner_id" yaml:"runner_id"`
	Plan     *CoarsePlan    `json:"plan,omitempty" yaml:"plan,omitempty"`
	State    RunnerState    `json:"state" yaml:"state"`
	Phase    RunnerPhase    `json:"phase" yaml:"phase"`
	Snapshot RunnerSnapshot `json:"snapshot" yaml:"snapshot"`
}

func buildInitialRunnerSnapshot(nodes map[string]*Node) RunnerSnapshot {
	snapshot := RunnerSnapshot{
		CompletedNodes: make(map[string]any),
		PendingNodes:   make(map[string]int, len(nodes)),
		GraphEdges:     make(map[string][]string),
		NodesData:      make(map[string]*Node, len(nodes)),
	}
	for id, node := range nodes {
		snapshot.NodesData[id] = node
		snapshot.PendingNodes[id] = len(node.Parents)
		for _, parent := range node.Parents {
			snapshot.GraphEdges[parent.Id] = append(snapshot.GraphEdges[parent.Id], id)
		}
	}
	return snapshot
}

func cloneRunnerSnapshot(snapshot RunnerSnapshot) RunnerSnapshot {
	copySnapshot := RunnerSnapshot{
		ActiveCount:    snapshot.ActiveCount,
		CompletedNodes: make(map[string]any, len(snapshot.CompletedNodes)),
		PendingNodes:   make(map[string]int, len(snapshot.PendingNodes)),
		GraphEdges:     make(map[string][]string, len(snapshot.GraphEdges)),
		NodesData:      make(map[string]*Node, len(snapshot.NodesData)),
	}
	for id, output := range snapshot.CompletedNodes {
		copySnapshot.CompletedNodes[id] = output
	}
	for id, pending := range snapshot.PendingNodes {
		copySnapshot.PendingNodes[id] = pending
	}
	for id, children := range snapshot.GraphEdges {
		copySnapshot.GraphEdges[id] = append([]string(nil), children...)
	}
	for id, node := range snapshot.NodesData {
		copySnapshot.NodesData[id] = node
	}
	return copySnapshot
}

// IsRemovable reports whether a runner in the given state may be removed
// from an upper-layer registry. Running or idle runners are still live and
// should not be dropped.
func IsRemovable(state RunnerState) bool {
	switch state.Status {
	case RunnerStatusPending, RunnerStatusFailed, RunnerStatusCanceled:
		return true
	default:
		return false
	}
}
