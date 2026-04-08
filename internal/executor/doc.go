// Package executor implements the in-tool runtime entrypoints.
//
// [Executor.Describe] emits tool metadata for registration, and [Executor.Run]
// executes one assigned sub-task using the scheduler-provided environment
// contract. Tools may provide their own run hook; when they do not, this
// package writes scaffold artifacts and a valid _handoff.md so orchestration
// flows remain testable.
package executor
