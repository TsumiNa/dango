package runner

// CoarsePlan is the Orchestrator's high-level task graph before execution
// starts.
//
// A plan pairs the original request with the ordered node graph the
// Orchestrator's planner produced. RunnerID is populated once the plan has
// been materialized into a Runner.
type CoarsePlan struct {
	Request  string           `json:"request" yaml:"request"`
	RunnerID string           `json:"runner_id,omitempty" yaml:"runner_id,omitempty"`
	Nodes    []CoarsePlanNode `json:"nodes" yaml:"nodes"`
}

// CoarsePlanNode describes one executor-sized unit in a [CoarsePlan].
type CoarsePlanNode struct {
	ID              string   `json:"id" yaml:"id"`
	SkillName       string   `json:"skill_name" yaml:"skill_name"`
	TaskDescription string   `json:"task_description" yaml:"task_description"`
	DependsOn       []string `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
}

// CloneCoarsePlan returns a deep copy of plan so callers can mutate the
// result without affecting the original.
func CloneCoarsePlan(plan *CoarsePlan) *CoarsePlan {
	if plan == nil {
		return nil
	}
	copyPlan := *plan
	copyPlan.Nodes = make([]CoarsePlanNode, len(plan.Nodes))
	for i, node := range plan.Nodes {
		copyPlan.Nodes[i] = node
		copyPlan.Nodes[i].DependsOn = append([]string(nil), node.DependsOn...)
	}
	return &copyPlan
}
