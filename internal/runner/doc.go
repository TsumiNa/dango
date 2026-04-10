// Package runner contains the execution plane for dango task runs.
//
// The package begins where the orchestrator stops. Once a task request has
// been normalized and persisted, runner owns the long-running execution
// lifecycle: creating and resuming in-memory runners, deriving or reviewing the
// DAG plan, asking executors to refine stage detail, dispatching ready edges,
// collecting handoffs, and finalizing the task result. The most important entry
// points are [TaskRunnerService], [TaskRunner], [Planner], [Scheduler], and
// [StateMachine].
//
// The normal workflow is layered. [TaskRunnerService] creates a persisted task
// through the orchestrator-facing task store and then either starts it in the
// background or executes it synchronously. [TaskRunner.Run] performs the
// lifecycle for one task: update status, ask [Planner] for a reviewed
// spec.DAGPlan, persist that plan, execute it through [StateMachine], and
// finalize result artifacts and terminal handoff summaries. [Planner] itself is
// a pipeline of draft planning, executor-side refinement, and review or repair,
// while [Scheduler] is the bridge from one planned edge to the runtime package.
// [StateMachine] coordinates those pieces by launching ready edges as soon as
// their dependencies are satisfied.
//
// Dependency direction matters here. The runner depends on task persistence,
// the runtime abstraction, taskflow contracts, llm-backed planning hooks, and
// spec types, but orchestrator should depend on runner only for control
// operations such as start, resume, describe, cancel, and clone. That split is
// what keeps HTTP request handling and durable control state in the control
// plane while leaving plan execution and edge supervision in the execution
// plane.
package runner
