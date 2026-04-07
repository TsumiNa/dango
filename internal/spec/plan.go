package spec

import "time"

type DAGPlan struct {
	Planner   string        `json:"planner,omitempty" yaml:"planner,omitempty"`
	Mode      string        `json:"mode,omitempty" yaml:"mode,omitempty"`
	CreatedAt time.Time     `json:"created_at" yaml:"created_at"`
	Edges     []PlannedEdge `json:"edges" yaml:"edges"`
}

type PlannedEdge struct {
	ID         string `json:"id" yaml:"id"`
	ToolName   string `json:"tool_name" yaml:"tool_name"`
	Upstream   string `json:"upstream,omitempty" yaml:"upstream,omitempty"`
	InputType  string `json:"input_type" yaml:"input_type"`
	OutputType string `json:"output_type" yaml:"output_type"`
	SubTask    string `json:"sub_task" yaml:"sub_task"`
}
