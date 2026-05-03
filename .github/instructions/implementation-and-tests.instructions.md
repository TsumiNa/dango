---
description: "Use when implementing user-described features, fixing bugs, refactoring construction APIs, or generating new code. Enforces minimal abstraction, minimal scope expansion, option ownership semantics, and core-logic test coverage with colocated `<source>_test.<ext>` files."
applyTo: "**"
---

# Implementation and Core-Logic Tests

When implementing what the user described, follow these principles together. They constrain both the production code and the accompanying tests.

## 1. Minimal Abstraction

Use the most direct code structure that satisfies the described requirement.

- Do not introduce interfaces, factories, registries, generics, plugin layers, or extra indirection unless the described requirement actually needs them.
- Do not split a single concrete implementation into multiple layers "for future flexibility".
- Inline a value or short helper when extracting it would only be used once and would not improve readability.
- Prefer concrete types and direct function calls over abstract types and dispatch when there is one real implementation today.
- Do not split a single-use operation into a public/private wrapper pair or a chain of one-off helper functions just to make individual functions shorter or easier to unit test. If the helper has no independent semantic role, no second caller, and no meaningful name beyond restating the caller, keep the logic in the caller.
- Prefer writing one cohesive function for one cohesive behavior. Extract a helper only when it names a distinct concept, removes meaningful duplication, isolates a genuinely complex sub-step, or is reused by multiple call sites.

If you find yourself adding a layer "in case we need to swap it later", stop and use the concrete form instead.
If you find yourself creating a function that only forwards to another function with nearly the same name, inline it unless there is a concrete lifecycle, validation, locking, instrumentation, or API-boundary reason for the wrapper.

### Constructor Shape, Config, and Options

Prefer the construction shape `New(required ..., cfg Cfg, opts ...Opt) *S` when a type needs both required inputs and optional tuning.

- The leading required parameters are the concrete things the constructor needs to solve the problem or use its tools. They are not configuration. Examples include a `client`, instructions, source text, a filesystem root, or the executable tools a value must use.
- `Cfg`/`Config` controls behavioral choices for how the constructor or resulting value uses those required inputs, such as limits, truncation policy, filtering, streaming mode, retry policy, or other public knobs.
- `opts ...Opt` applies extra adjustments to the final instance after the config has been resolved and the instance has been built.

Every caller-facing `Cfg`/`Config` struct should contain only exported fields and should provide a default entry point such as `Default`, `DefaultCfg`, or `DefaultConfig` following the package's local naming style. Callers should be able to either write a struct literal directly or start from the default value and adjust fields before passing it to `New`.

Use `WithXxx(...)` option functions to adjust fields on the final instance produced from `Cfg`; do not use them to mutate the caller's config object. For example, if `New(Cfg{A: "hello"}, WithB(2))` constructs `S{a: "hello"}`, the option should set the returned instance's private `b` field to `2`. This gives private fields a deliberate construction-time control boundary.

- Prefer `WithXxx(...)` for optional final-instance field adjustments that are clearer than exposing the field directly through `Cfg`.
- Do not use `WithXxx(...)` as a mechanism for rewriting or patching the caller's config value. Normalize or copy config first, build the instance, then apply options to that instance.
- After construction, private fields remain immutable to callers unless the type exposes a public method for changing them. Construction-time controls belong in `WithXxx(...)`; runtime controls belong in explicit public methods.
- If a `WithXxx(...)` option installs an externally owned pointer, mutable object, stream, store, client, logger, context, callback, tool, or other live dependency into the instance, its doc comment must explicitly describe ownership and race expectations. Call out whether the constructed instance keeps a reference, whether the caller may mutate or close/cancel it after construction, and who is responsible for synchronization.
- If the externally owned object does not document its own concurrency safety, the `WithXxx(...)` comment must warn that sharing or mutating it concurrently can race unless the caller provides synchronization or the constructed instance serializes access.
- When a module owns a runtime object such as a stream, the default shape should still be for the module to create it and expose an accessor when callers need to observe it. If an option intentionally installs a caller-owned runtime object instead, document that the object is shared rather than owned by the constructed module.
- Variadic options remain appropriate for short-lived operation settings such as subscription replay/buffer policies when they adjust that operation's final settings rather than mutating a config object.

### Go Type Placement

When writing or refactoring Go, place types near the behavior that gives them meaning instead of collecting them in a package-wide `types.go` by default.

- Keep a primary type with its constructor, exported API, methods, and tightly coupled supporting types in the same cohesive source file.
- Put request/response/config/result structs next to the function or component that consumes and returns them when they belong to one workflow.
- Keep small private helper types next to the function or method that uses them unless they are reused across multiple files.
- Avoid catch-all `types.go` files that mix unrelated errors, aliases, request DTOs, state enums, and result structs while other files also define their own local types. This split makes ownership unclear and forces readers to jump between files.
- Use a package-level `types.go` only when the package has a small, stable vocabulary of shared domain types that is genuinely used across the package and is clearer together than colocated with one implementation file. If only some types are shared, move the feature-specific ones back beside their behavior.
- Do not move a type into `types.go` just because it is exported. Exported types should still live with their main behavior when that behavior is localized.

## 2. Minimal Scope Expansion

Implement only what the user's description asks for.

- Do not add configuration options, flags, fields, methods, or endpoints that the description does not mention.
- Do not refactor unrelated code, rename unrelated symbols, or "clean up" nearby files while implementing the requested change.
- Do not add logging, metrics, retries, caching, or validation beyond what the description or existing surrounding code already implies.
- If a related improvement seems valuable but is out of scope, mention it briefly to the user instead of silently adding it.

When in doubt, prefer the smaller change. The user can always ask for more.

## 3. Tests Cover Core Logic and Failure Patterns

Every implementation change must ship with at least one test file that exercises the core logic, with priority on the patterns most likely to fail.

- Always generate or update a test file for the code you wrote or modified.
- Cover the primary success path of the described behavior.
- Cover the inputs and conditions most likely to break it: empty / missing / malformed input, boundary values, error returns from dependencies, and any explicit precondition the code enforces (for example "must be a directory", "must be non-nil", "must contain required file").
- Do not aim for exhaustive coverage of trivial getters, generated code, or thin pass-through wrappers. Aim for the logic that, if broken, would silently produce wrong behavior.
- Run the new tests and confirm they pass before reporting the task done.

### Test File Naming and Location

Colocate tests next to the source file and name them after that source file.

- For a source file `foo.go`, create or update `foo_test.go` in the same package and directory.
- For a source file `bar.py`, create or update `bar_test.py` in the same directory.
- For other languages, use the analogous `<basename>_test.<ext>` pattern in the same directory.
- Do not create a separate `tests/` tree, a generic `helpers_test.<ext>`, or a catch-all file when a focused `<source>_test.<ext>` companion already fits.
- If a single source file's tests grow large enough to warrant splitting, split by feature into additional files that still start with the source file's basename (for example `foo_parse_test.go`, `foo_validate_test.go`).

## Quick Self-Check Before Finishing

Before reporting an implementation as complete, confirm:

1. No abstraction was added that is not justified by the described requirement.
2. No feature, option, or refactor was added beyond the description.
3. Constructor APIs separate required problem inputs, public `Cfg`/`Config` behavioral knobs, and `WithXxx(...)` final-instance adjustments.
4. Caller-facing `Cfg`/`Config` structs contain only exported fields and provide a default entry point.
5. `WithXxx(...)` options adjust final instance fields, not caller config objects, and any option that stores an externally owned mutable/live object documents ownership and race expectations.
6. A `<source>_test.<ext>` file exists next to the changed source and exercises the core path plus the most likely failure patterns.
7. The new and existing tests for the touched packages pass.