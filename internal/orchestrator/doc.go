// Package orchestrator contains the control-plane services for dango.
//
// The package includes tool registration, task persistence, and HTTP serving.
// Runner-owned planning and execution are delegated to the runner package.
// Primary entrypoints are [RegistryService], [TaskService], and [Server].
package orchestrator
