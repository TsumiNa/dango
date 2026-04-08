package spec

import "time"

// DAGPlan is the orchestrator's persisted execution plan for a task.
type DAGPlan struct {
	// Planner identifies the planner implementation that produced the plan.
	Planner string `json:"planner,omitempty" yaml:"planner,omitempty"`
	// Mode describes the overall plan topology, such as linear or fan-out.
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
	// CreatedAt records when the plan was generated.
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	// Edges lists the planned execution edges in planner-defined order.
	Edges []PlannedEdge `json:"edges" yaml:"edges"`
}

// PlannedEdge describes one tool invocation within a DAG plan.
type PlannedEdge struct {
	// ID uniquely identifies the edge within the task.
	ID string `json:"id" yaml:"id"`
	// ToolName identifies the tool to invoke for this edge.
	ToolName string `json:"tool_name" yaml:"tool_name"`
	// Upstream identifies the upstream edge whose output becomes this edge's input.
	Upstream string `json:"upstream,omitempty" yaml:"upstream,omitempty"`
	// InputType captures the logical input type expected by the tool.
	InputType string `json:"input_type" yaml:"input_type"`
	// OutputType captures the logical output type produced by the tool.
	OutputType string `json:"output_type" yaml:"output_type"`
	// SubTask contains the sub-task markdown given to the tool.
	SubTask string `json:"sub_task" yaml:"sub_task"`
}
