package spec

// TaskStatus tracks the lifecycle of a task in the orchestrator.
type TaskStatus string

const (
	// TaskStatusPlanning means the orchestrator has created the task and is planning it.
	TaskStatusPlanning TaskStatus = "planning"
	// TaskStatusCompleting means tools are refining or completing a plan.
	TaskStatusCompleting TaskStatus = "completing"
	// TaskStatusApproved means the plan has passed the orchestration approval gate.
	TaskStatusApproved TaskStatus = "approved"
	// TaskStatusExecuting means the scheduler is running the task's edges.
	TaskStatusExecuting TaskStatus = "executing"
	// TaskStatusDone means all terminal edges finished successfully enough to produce a result.
	TaskStatusDone TaskStatus = "done"
	// TaskStatusFailed means planning or execution ended in failure.
	TaskStatusFailed TaskStatus = "failed"
)

// EdgeStatus tracks the lifecycle of an individual DAG edge.
type EdgeStatus string

const (
	// EdgeStatusPending means the edge has not started yet.
	EdgeStatusPending EdgeStatus = "pending"
	// EdgeStatusRunning means the edge is currently executing.
	EdgeStatusRunning EdgeStatus = "running"
	// EdgeStatusCompleted means the edge completed and wrote a valid handoff.
	EdgeStatusCompleted EdgeStatus = "completed"
	// EdgeStatusFailed means the edge ended in failure.
	EdgeStatusFailed EdgeStatus = "failed"
)
