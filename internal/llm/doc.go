// Package llm contains provider clients, shared AI hook contracts, and
// structured hook errors used by dango's dynamic planning and execution flows.
//
// Architecturally, this package is where the repository centralizes everything
// that is common to model-backed stages without tying callers to one specific
// provider. [Client] and [Request] define the low-level JSON-completion API
// used by the built-in AI paths, while the hook interfaces such as
// [IntentUnderstandingHook], [DraftPlanningHook], [ReviewPlanningHook],
// [DetailPlanningHook], and [ExecuteGenerationHook] define the higher-level
// contracts shared by the orchestrator, runner, and executor packages.
// [CannotProceedError] gives those stages a consistent way to report that a
// dynamic step could not produce a valid result.
//
// The typical workflow is: construct a [Client] with [NewOpenAICompatible] or
// [NewOpenAICompatibleFromEnv], render a structured prompt in a caller package,
// submit a [Request] through [Client.CompleteJSON], unmarshal the returned JSON
// into the hook-specific result type, and validate the result before using it
// to mutate task state. Packages above llm own prompt construction and result
// validation; this package owns transport, shared contracts, and error shape.
//
// Dependency direction is important here. The orchestrator, runner, executor,
// and prompts packages may all depend on llm, but llm should stay free of
// orchestration policy and business logic. That keeps model transport,
// environment-derived configuration, and hook contracts reusable even as the
// planning and execution layers evolve.
package llm
