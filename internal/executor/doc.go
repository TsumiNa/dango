// Package executor implements the in-tool runtime entrypoints.
//
// [Executor.Describe] emits tool metadata for registration, and [Executor.Run]
// executes one assigned sub-task using the scheduler-provided environment
// contract. Tools may provide their own plan and run hooks; when the required
// hook is absent, the executor falls back to repository-owned built-in AI
// detail-planning or execute-generation. When neither hooks nor built-in AI
// can produce a valid result, execution fails with an explanatory handoff.
package executor
