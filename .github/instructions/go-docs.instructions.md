---
description: "Use when writing, editing, or reviewing Go doc comments, package docs, field docs, or Example tests. Write idiomatic Go documentation for go doc, pkg.go.dev, and local pkgsite."
applyTo: "**/*.go"
---

# Go Documentation

Write idiomatic Go documentation for `go doc`, `pkg.go.dev`, and local `pkgsite`. Treat source doc comments as the single source of truth for API behavior.

- Document at the package level, not the file-module level.
- Put package documentation in one `doc.go` file and start it with `Package <name> ...`.
- Exported names should have doc comments unless there is a deliberate, local reason to omit one.
- For exported functions and methods, comments may be more complete and should explain behavior, contract, and important usage expectations. For unexported functions and methods, keep comments concise, and omit comments for obvious private helpers whose purpose is already clear from the name and local context.
- Start each type, func, method, const, or var comment with the declared name.
- Use complete sentences. Explain behavior, semantics, invariants, lifecycle, concurrency guarantees, and zero-value usability when relevant rather than restating what is already obvious from the signature.
- Field comments should explain exported fields when the meaning is not already obvious from the field name and type.
- Do not use Python-style `Args:`, `Parameters:`, or `Returns:` sections unless explicitly requested. In Go, parameter and result details should usually be described naturally in prose.
- If a function needs long per-parameter explanation, prefer improving names, introducing an options/config struct, or redesigning the API before adding bulky comment blocks.
- Keep repository navigation and high-level onboarding in `README.md`, but keep API truth in source doc comments.
- Prefer short executable `Example...` tests in `_test.go` for usage examples. Keep them copy-paste friendly and include `// Output:` when the example is testable.
- Optimize comments for rendered output in `go doc` and `pkgsite`. When documentation quality matters, verify how it renders locally.

When documenting a type like `Executor`, the type comment should describe what the type is responsible for, whether its zero value is usable, and any relevant expectations around reuse, lifecycle, or concurrency.