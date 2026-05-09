package persistence

// PathRule maps a runner ID to a per-runner workspace subdirectory under the
// global persistence root.
type PathRule func(runnerID string) string

// DefaultPathRule returns the default per-runner subdirectory name.
func DefaultPathRule(runnerID string) string {
	return "task_" + runnerID
}
