---
description: "Use when implementing user-described features, fixing bugs, or generating new code. Enforces minimal abstraction, minimal scope expansion, and core-logic test coverage with colocated `<source>_test.<ext>` files."
applyTo: "**"
---

# Minimal Implementation and Core-Logic Tests

When implementing what the user described, follow these three principles together. They constrain both the production code and the accompanying tests.

## 1. Minimal Abstraction

Use the most direct code structure that satisfies the described requirement.

- Do not introduce interfaces, factories, registries, generics, plugin layers, or extra indirection unless the described requirement actually needs them.
- Do not split a single concrete implementation into multiple layers "for future flexibility".
- Inline a value or short helper when extracting it would only be used once and would not improve readability.
- Prefer concrete types and direct function calls over abstract types and dispatch when there is one real implementation today.

If you find yourself adding a layer "in case we need to swap it later", stop and use the concrete form instead.

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
3. A `<source>_test.<ext>` file exists next to the changed source and exercises the core path plus the most likely failure patterns.
4. The new and existing tests for the touched packages pass.
