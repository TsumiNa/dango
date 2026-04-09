package spec

// TaskStatus tracks the lifecycle of a task in the orchestrator.
type TaskStatus string

const (
	// TaskStatusPending means the orchestrator has created the task runner but execution has not started.
	TaskStatusPending TaskStatus = "pending"
	// TaskStatusPlanning means the orchestrator has created the task and is planning it.
	TaskStatusPlanning TaskStatus = "planning"
	// TaskStatusReviewing means the runner is reviewing or adjusting the refined workflow before execution.
	TaskStatusReviewing TaskStatus = "reviewing"
	// TaskStatusApproved means the plan has passed the orchestration approval gate.
	TaskStatusApproved TaskStatus = "approved"
	// TaskStatusExecuting means the scheduler is running the task's edges.
	TaskStatusExecuting TaskStatus = "executing"
	// TaskStatusPaused means the runner is waiting for an explicit resume signal.
	TaskStatusPaused TaskStatus = "paused"
	// TaskStatusDone means all terminal edges finished successfully enough to produce a result.
	TaskStatusDone TaskStatus = "done"
	// TaskStatusCanceled means the task was explicitly canceled by orchestration control.
	TaskStatusCanceled TaskStatus = "canceled"
	// TaskStatusCloned means the task history was superseded by a cloned runner lineage.
	TaskStatusCloned TaskStatus = "cloned"
	// TaskStatusFailed means planning or execution ended in failure.
	TaskStatusFailed TaskStatus = "failed"
)

// EdgeStatus tracks the lifecycle of an individual DAG edge.
type EdgeStatus string

const (
	// EdgeStatusPending means the edge has not started yet.
	EdgeStatusPending EdgeStatus = "pending"
	// EdgeStatusPlanning means the edge is being refined during executor planning.
	EdgeStatusPlanning EdgeStatus = "planning"
	// EdgeStatusRunning means the edge is currently executing.
	EdgeStatusRunning EdgeStatus = "running"
	// EdgeStatusCompleted means the edge completed and wrote a valid handoff.
	EdgeStatusCompleted EdgeStatus = "completed"
	// EdgeStatusCanceled means the edge was canceled before successful completion.
	EdgeStatusCanceled EdgeStatus = "canceled"
	// EdgeStatusFailed means the edge ended in failure.
	EdgeStatusFailed EdgeStatus = "failed"
)
