---
description: "Use when writing, editing, or reviewing Go doc comments, package docs, field docs, or Example tests. Write idiomatic Go documentation for go doc, pkg.go.dev, and local pkgsite."
applyTo: "**/*.go"
---

# Go Documentation

Write idiomatic Go documentation for `go doc`, `pkg.go.dev`, and local `pkgsite`. Treat doc comments as production API docs and the single source of truth for package and symbol behavior.

## General Rules

- Prefer line comments `//` over block comments for documentation.
- Write clear English prose in complete sentences.
- Document packages, not file-modules. In Go, the primary documentation unit is the package.
- Doc comments belong immediately above the package, const, var, type, or func declaration they document.
- Explain semantics, invariants, side effects, concurrency guarantees, nil and zero-value behavior, error meaning, ownership and lifetime expectations, and cancellation or timeout behavior when relevant.
- Do not restate information that is already obvious from the signature unless it matters for semantics.

## Package Docs

- Every public package should have exactly one package doc comment.
- Put package documentation in `doc.go` unless there is a strong reason not to.
- Start package docs with `Package <name> ...`.
- Package docs should be complete enough to stand on their own in `pkgsite`: include a one-sentence summary, what the package is for, important constraints or guarantees, the main entry points to read first, and a short usage snippet only when it adds real value.
- When a package is architectural or workflow-heavy, package docs should also explain the package's role in the larger system, the typical call or lifecycle flow through the package, and the dependency direction between its core types, functions, or neighboring packages.
- For internal infrastructure packages, prefer package docs that help a new maintainer answer three questions quickly: why this package exists, how it is normally entered and used, and which other packages it coordinates with or deliberately does not own.
- It is acceptable for package docs to be several paragraphs long when that is what is required to accurately describe architecture, workflow, invariants, and the relationships between the package's primary exported types.
- Do not spread package docs across multiple files.

## Symbol Docs

- Every exported type, function, method, const, and var should have a useful doc comment unless there is a deliberate, local reason to omit one.
- Start each type, func, method, const, or var comment with the declared name.
- For exported functions and methods, comments may be more complete and should explain behavior, contract, and important usage expectations. For unexported functions and methods, keep comments concise, and omit comments for obvious private helpers whose purpose is already clear from the name and local context.
- Put important usage semantics on the type when possible; keep method comments focused on method-specific behavior.
- For exported structs, interfaces, and other types, document what the type represents and mention zero-value usability, concurrency safety, lifecycle, and invariants when relevant.
- Field comments should explain exported fields when the meaning is not already obvious from the field name and type. For config and options structs, field comments should carry most of the per-field explanation.
- If a function needs long per-parameter explanation, prefer improving names, introducing an options or config struct, or redesigning the API before adding bulky comment blocks.
- Do not use Python-style `Args:`, `Parameters:`, `Returns:`, or `Raises:` sections unless explicitly requested. In Go, parameter and result details should usually be described naturally in prose.
- Use Go doc links like `[Client]`, `[New]`, or `[Parse]` when they improve readability in rendered docs.

## Examples And Rendering

- Prefer short executable `Example...` tests in `_test.go` for usage examples. Keep them copy-paste friendly and include `// Output:` when the example is testable.
- Use comments for semantics and examples for usage patterns.
- Keep repository navigation and high-level onboarding in `README.md`, but keep API truth in source doc comments.
- Long-form tutorials or architecture notes may live in `docs/`, but API reference should still come from source comments.
- Optimize comments for rendered output in `go doc` and `pkgsite`. When documentation quality matters, verify how it renders locally.

When documenting a type like `Executor`, the type comment should describe what the type is responsible for, whether its zero value is usable, and any relevant expectations around reuse, lifecycle, or concurrency.