package spec

import "time"

// DAGPlan is the orchestrator's persisted execution plan for a task.
//
// It is the shared contract produced by the runner planning pipeline, stored by
// task services, and later consumed by the scheduler and state machine. The
// plan captures both workflow-level metadata and the ordered edge definitions
// needed to reconstruct execution.
type DAGPlan struct {
	// Planner identifies the planner implementation that produced the plan.
	Planner string `json:"planner,omitempty" yaml:"planner,omitempty"`
	// Mode describes the overall plan topology, such as linear or fan-out.
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
	// Revision tracks monotonic runner edits to the DAG within one task lineage.
	Revision int `json:"revision,omitempty" yaml:"revision,omitempty"`
	// CreatedAt records when the plan was generated.
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	// ReviewedAt records when the runner finished its last plan review pass.
	ReviewedAt time.Time `json:"reviewed_at,omitempty" yaml:"reviewed_at,omitempty"`
	// Edges lists the planned execution edges in planner-defined order.
	Edges []PlannedEdge `json:"edges" yaml:"edges"`
}

// PlannedEdge describes one tool invocation within a DAG plan.
//
// Each edge starts as a draft planner selection and is then enriched by
// executor-side detail planning with stage-local sub-task text, expected
// outputs, and user-facing summaries before the runner executes it.
type PlannedEdge struct {
	// ID uniquely identifies the edge within the task.
	ID string `json:"id" yaml:"id"`
	// ToolName identifies the tool to invoke for this edge.
	ToolName string `json:"tool_name" yaml:"tool_name"`
	// Dependencies identifies the upstream edges whose outputs become this edge's inputs.
	Dependencies []string `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
	// InputType captures the logical input type expected by the tool.
	InputType string `json:"input_type" yaml:"input_type"`
	// OutputType captures the logical output type produced by the tool.
	OutputType string `json:"output_type" yaml:"output_type"`
	// Title is the concise runner-visible name for the edge.
	Title string `json:"title,omitempty" yaml:"title,omitempty"`
	// Summary is the executor-refined description of the edge's intent.
	Summary string `json:"summary,omitempty" yaml:"summary,omitempty"`
	// ExpectedOutputs lists the artifacts the executor expects to materialize.
	ExpectedOutputs []string `json:"expected_outputs,omitempty" yaml:"expected_outputs,omitempty"`
	// SubTask contains the sub-task markdown given to the tool.
	SubTask string `json:"sub_task" yaml:"sub_task"`
}
