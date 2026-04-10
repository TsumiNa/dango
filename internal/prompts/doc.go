// Package prompts stores the repository-owned prompt assets for dango's
// built-in AI hooks.
//
// The package sits between the orchestration layers and the llm transport
// layer. It does not call models directly. Instead, it embeds the markdown
// prompt templates that define the repository's default behavior for intent
// understanding, draft planning, plan review, plan repair, executor detail
// planning, and executor execute-time generation, then exposes rendering
// functions such as [RenderIntentUnderstand], [RenderPlannerDraft],
// [RenderPlannerReview], [RenderPlannerRepair], [RenderExecutorDetailPlan], and
// [RenderExecutorExecute].
//
// The normal workflow is: a caller package assembles structured context from
// persisted requests, tool catalogs, task plans, or executor runtime state,
// passes that data to one of the Render* functions, and submits the returned
// prompt through an llm.Client. Validation of the model response happens in
// the caller, not here. That keeps this package deterministic and easy to test:
// given the same inputs it always renders the same prompt text.
//
// Dependency direction should remain one-way. Packages such as orchestrator,
// runner, and executor may depend on prompts, but prompts should not depend on
// those higher-level packages for control flow. Its job is to encode the
// repository's built-in prompt assets and the minimal rendering helpers needed
// to fill them with structured data.
package prompts
