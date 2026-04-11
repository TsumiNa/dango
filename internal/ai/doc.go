// Package ai contains the OpenAI-compatible client, shared AI types, and
// structured error reporting used by dango's planning and execution flows.
//
// This package is the central AI transport layer. [Client] and [Request] define
// the low-level JSON-completion API shared by the orchestrator, runner, and
// executor. [CannotProceedError] gives AI-backed stages a consistent way to
// report that a step could not produce a valid result.
//
// The typical workflow is: construct a [Client] with [NewOpenAICompatible] or
// [NewOpenAICompatibleFromEnv], render a structured prompt in a caller package,
// submit a [Request] through [Client.CompleteJSON], unmarshal the returned JSON
// into the caller-specific result type, and validate the result before using it
// to mutate task state. Packages above ai own prompt construction and result
// validation; this package owns transport, configuration, and error shape.
//
// Dependency direction: orchestrator, runner, executor, and prompts packages
// may all depend on ai, but ai should remain free of orchestration policy and
// business logic.
package ai
