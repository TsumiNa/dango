// Package orchestrator contains the control-plane services for dango.
//
// The package includes tool registration, task persistence, task runner
// planning, and HTTP serving. Execution dispatch is delegated to the runner
// package. Primary entrypoints are [RegistryService], [TaskService],
// [Planner], [TaskRunner], [TaskRunnerService], and [Server].
package orchestrator
