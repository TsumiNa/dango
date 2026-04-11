// Package executor implements the in-tool runtime entrypoints used by
// registered tools.
//
// This package is the worker-side half of dango's execution model. The runner
// invokes tools through two phases: planning and execution. [Executor.Describe]
// emits the local tool specification used during registration,
// [Executor.Plan] refines one planned stage into an executable
// spec.ExecutorPlan, and [Executor.Run] carries out the assigned sub-task and
// writes the resulting artifacts and handoff files. The package therefore sits
// at the boundary where a tool's local implementation meets the runner's
// scheduling contract.
//
// The normal workflow begins with [New], which constructs an [Executor] around
// output writers, a logger, and the built-in LLM client factory. Describe loads
// the local tool spec and serializes it for the registry. Plan loads the
// scheduler-provided runtime context, prefers a tool-provided planning hook
// when one exists, and otherwise falls back to repository-owned built-in AI via
// the llm and prompts packages. Run follows the same shape for execution: it
// validates the runtime contract, prepares output directories, prefers an
// explicit run hook, and otherwise performs execute-time generation with the
// built-in AI path.
//
// The key dependency relationship is that the runner owns task lifecycle and
// dispatch, while this package owns only tool-local behavior. It consumes
// spec.ToolSpec, spec.ExecutorPlan, and spec.Handoff as machine
// contracts, uses the llm package to represent hook failures and LLM requests,
// and renders prompts through package prompts. A successful run must materialize
// the public handoff, the private handoff, and any declared output files so the
// runner can persist edge state and feed downstream stages.
package executor
