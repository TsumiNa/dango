package spec

// ExecutorPlan describes the executor-refined output of one planning step.
type ExecutorPlan struct {
	// Summary is the concise description of the work the executor intends to perform.
	Summary string `json:"summary" yaml:"summary"`
	// SubTask is the finalized markdown brief that should be executed later.
	SubTask string `json:"sub_task" yaml:"sub_task"`
	// ExpectedOutputs lists the artifacts the executor expects to materialize.
	ExpectedOutputs []string `json:"expected_outputs,omitempty" yaml:"expected_outputs,omitempty"`
}