// Package runtime abstracts the backends used to describe and execute tools.
//
// This package is the runtime boundary shared by the orchestrator and runner.
// Callers depend on [ContainerRuntime], not on a specific backend. The default
// implementation is [MultiRuntime], created by [NewDefault], which dispatches
// requests to either a Docker-backed runtime or a host-local runtime depending
// on the tool reference. The planning and execution contracts are represented by
// [ExecutorPlanRequest] and [ExecutorRunRequest], which capture the host-side
// paths and identifiers the backend needs to invoke a tool.
//
// The typical workflow is: the orchestrator or runner constructs the default
// runtime, asks it to pull or otherwise validate a tool reference, then calls
// [ContainerRuntime.DescribeTool], [ContainerRuntime.PlanExecutor], or
// [ContainerRuntime.RunExecutor] as the task moves from registration into
// planning and finally execution. [MultiRuntime] resolves `host://` references
// to the host backend and sends all other references through Docker, which lets
// the higher layers stay agnostic about where the tool actually runs.
//
// This package therefore owns backend selection, mount and path conventions for
// executor invocations, and transport-specific execution details. The runner
// still owns scheduling and lifecycle state, while orchestrator still owns
// registry persistence. Runtime only provides the common execution interface
// that those higher-level packages depend on.
package runtime
