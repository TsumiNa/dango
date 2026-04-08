// Package runtime abstracts tool description and execution backends.
//
// Callers target [ContainerRuntime] and use [MultiRuntime] to dispatch between
// Docker-compatible container execution and host-local demo execution
// (`host://` references). This keeps orchestrator logic independent from the
// underlying runtime implementation.
package runtime
