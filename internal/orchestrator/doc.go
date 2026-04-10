// Package orchestrator contains the control-plane services for dango.
//
// The package owns the parts of the system that accept requests, mutate durable
// control-plane state, expose APIs, and manage the registry of available tools.
// Its primary entry points are [RegistryService], [TaskService], and [Server].
// [RegistryService] is responsible for turning an image or host reference into
// persisted registry state, [TaskService] persists task rows and filesystem
// artifacts, and [Server] exposes those capabilities over HTTP while delegating
// execution to the runner package. Built-in request normalization enters the
// package through [NewIntentUnderstandingHook].
//
// The typical request workflow is: the CLI bootstraps a data directory and
// store, constructs the orchestrator services, then calls [Server.ListenAndServe].
// Incoming requests are normalized into taskflow.RequestEnvelope values,
// optionally passed through the built-in intent hook, persisted by
// [TaskService], and then handed to runner.TaskRunnerService for background
// or synchronous execution. Registration follows a parallel control-plane flow:
// [RegistryService.Register] asks the runtime package to pull and describe a
// tool, merges any override with spec.MergeToolSpec, writes the registry
// files under the data directory, and persists the final row in SQLite.
//
// Dependency direction is a central architectural rule for this package. The
// orchestrator may create and control runners, but it should not own
// step-by-step execution mechanics once a task has been launched. Execution
// state transitions, edge dispatch, and handoff processing live in the runner
// package. Package orchestrator instead remains the durable control surface
// that turns API requests into persisted task and registry state.
package orchestrator
