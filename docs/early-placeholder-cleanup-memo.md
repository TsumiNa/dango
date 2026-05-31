# Memo: early-dev placeholders leaked into the real LLM path (deferred — BUG)

Status: **known bug**, deferred. Clean up as part of the planned contract-schema
rework + builtin prompt / skills optimization, and sweep for *all* early
placeholders at the same time (the one below is the first identified instance,
likely not the only one).

## The bug

`Agent.planTask()` (`agent/agent.go`) is leftover early-dev scaffolding
that writes two **hardcoded, task-independent** strings and bumps the version:

```go
func (e *Agent) planTask() error {
	e.logf("Planning a task...")
	e.planner.Reason   = "The task requires processing data and generating a report."
	e.planner.Solution = "Use a data processing library to analyze the data and generate the report."
	e.planner.Version++
	return nil
}
```

`PolishPlan()` calls it, and these canned strings then flow into the **real**
execution path — they were never meant to reach production output:

1. **Into the LLM prompt.** `polishPrompt()` (`agent/prompt.go`)
   injects `planner.Reason` / `planner.Solution` as a `Current planner draft`
   section, so a bound model is fed irrelevant boilerplate as if it were a prior
   draft.
2. **Into handoff output.** `runPolishStage()` (`agent/stage.go`)
   builds `defaultBody` from `task/version/reason/solution`. When
   `runnableRuntimeSkill()` returns `ok=false` (no LLM bound), `body =
   defaultBody`, so the canned strings are emitted verbatim as the polish-stage
   handoff to the orchestrator — masquerading as genuine planner reasoning.

Nothing marks these as synthetic, so in real runs they read like authentic
analysis.

### Related: suppressed error log

`Agent.logf` always logs at `Debug`:

```go
func (e *Agent) logf(format string, args ...any) { e.logger.Debug(fmt.Sprintf(format, args...)) }
```

so the error-context call `e.logf("Error planning tasks: %v", err)`
(`agent/agent.go`) is demoted to Debug and invisible at default levels.
Today it is **dead** (planTask always returns nil), but it is latent: once
planTask does real work and can fail, the failure would be silently swallowed.

This is the same root cause — planTask is still a stub — so #1 and the logf level
should be resolved together.

## Cleanup plan (when reworking contract schema + builtin prompt/skills)

- Decide planTask's fate: implement real planning, fully delegate to the LLM
  skill (and drop the stub), or remove it. Stop writing canned Reason/Solution
  into the planner.
- Until then, do not let placeholder text reach the prompt or handoff output —
  leave the fields empty or clearly tag synthetic content.
- Fix `logf` so error-context messages log at `Error` (or call the logger
  directly at the right level), not `Debug`.
- **Sweep** the agent / runner / orchestrator for other early-dev placeholders
  (hardcoded prompts, canned summaries, stubbed reasoning) and handle them in the
  same pass.

Surfaced during PR #115 review; out of scope for that package-move refactor.
