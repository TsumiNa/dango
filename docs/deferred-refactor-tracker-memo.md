# Deferred Refactor Tracker Memo

Last updated: 2026-05-11

This memo records deferred refactor topics discussed before PR B.
PR B is handled separately and should proceed now.

## PR A - Logging Integration Refactor (Deferred)

Status: Deferred to next large refactor.

Current intent:

- Evolve `internal/logging` into an opt-in integration path.
- Expose a configured logger through package initialization flow.
- Add orchestrator option `WithLoggerCfg(...)` for system-wide logger setup and tuning.
- If callers do not set `WithLoggerCfg(...)`, keep logging package unused/off by default.

Open questions to resolve later:

- Whether logger exposure should rely on `init()` or explicit constructor wiring.
- Final ownership and lifecycle for logger config (startup-only vs runtime mutation).
- Interaction between existing `WithOrchestratorLogger(...)` and new `WithLoggerCfg(...)`.
- Backward compatibility policy for examples/tests using direct logger injection.

## PR C - Builtin Tools Restructure (Deferred)

Status: Deferred to independent refactor.

Current intent:

- Simplify llm runtime toolset around `bash` plus common utilities.
- Favor common shell tools used in real workflows (`awk`, `curl`, `grep`, `sed`, `xargs`, etc.).
- For repeated command patterns, consider higher-level wrapped tools to improve:
  - model accuracy
  - token efficiency
  - task success rate

Potential wrapper candidates:

- search/replace style wrappers for common pipelines such as `cat + grep + sed`.
- Focused read/filter/transform helpers where shell composition is noisy for the model.

Open questions to resolve later:

- Minimal default builtin set vs optional extended set.
- Safety boundaries and allowlist policy after introducing wrappers.
- How to measure token/correctness gains before landing broad changes.

## PR D - API Cleanup and Compatibility Review (Deferred)

Status: Deferred for dedicated discussion.

Current assessment:

- Multiple APIs flagged by deadcode need explicit keep/remove decisions.
- Some symbols may be intentional extension points despite low in-repo usage.

Decision criteria for later review:

- Keep if needed as near-term public/internal extension points.
- Remove if only test-facing legacy artifacts with no production path.
- Avoid compatibility layers unless explicitly required during that refactor.

## PR E - Streamrender Extraction and Terminal UI Refactor (Deferred)

Status: Deferred to independent package refactor after the immediate exchange/handoff fixes.

Current intent:

- Move `internal/streamrender` out of `internal` and make it a more independent rendering package.
- Use it as the foundation for future `cmd` terminal UI work.
- Separate terminal/UI rendering concerns from durable runtime message storage.
- Avoid treating renderer-captured markdown snippets as canonical exchange files.

Immediate bug context:

- The Honshu example configured `streamrender.ExchangeDir` as `artifacts/exchanges`, creating a second exchange-like directory outside runner persistence.
- `streamrender` currently writes any channel-looking markdown output as `exchange-<sequence>.md`; this captured an orchestrator planning handoff as a misleading `exchange-*` file.
- The current PR should only make a minimal fix: stop creating the separate outer exchanges directory and stop labeling non-exchange channel markdown as canonical exchange storage.

Open questions to resolve later:

- Public package name and API shape once `streamrender` leaves `internal`.
- Whether renderer output should write files at all, or only link to canonical runner persistence artifacts.
- How future terminal UI commands should discover request, runner, exchange, handoff, memo, and debug event paths.
- How much renderer state belongs in stream events versus external UI/session state.

## Immediate Action

- Finish the current exchange/handoff bug-fix PR with only the minimal
  `streamrender` containment fix; defer full renderer extraction and terminal
  UI architecture to PR E.
