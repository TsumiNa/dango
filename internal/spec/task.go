package spec

type TaskStatus string

const (
	TaskStatusPlanning   TaskStatus = "planning"
	TaskStatusCompleting TaskStatus = "completing"
	TaskStatusApproved   TaskStatus = "approved"
	TaskStatusExecuting  TaskStatus = "executing"
	TaskStatusDone       TaskStatus = "done"
	TaskStatusFailed     TaskStatus = "failed"
)

type EdgeStatus string

const (
	EdgeStatusPending   EdgeStatus = "pending"
	EdgeStatusRunning   EdgeStatus = "running"
	EdgeStatusCompleted EdgeStatus = "completed"
	EdgeStatusFailed    EdgeStatus = "failed"
)
