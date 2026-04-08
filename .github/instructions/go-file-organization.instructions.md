---
description: "Use when creating, editing, or refactoring Go files, especially when a file mixes a primary type with standalone helper functions. Prefer keeping a struct and its constructor and methods together while moving domain-specific helpers into separate files."
applyTo: "**/*.go"
---

# Go File Organization

Organize Go code by package responsibility and reader navigation, not by arbitrary file size.

- Keep a primary struct, its constructor, its exported API surface, and its methods in the same file when they form one cohesive unit.
- Keep tightly coupled supporting types in that same file when they primarily exist for the primary struct.
- Move standalone helper functions into separate files when they represent a distinct responsibility, such as runtime context loading, handoff writing, tool spec loading, validation, or conversion.
- Name helper files after the responsibility they contain, such as `runtime_context.go`, `handoff.go`, or `tool_spec.go`. Avoid generic names like `helpers.go` unless the package already uses that pattern and the helpers truly belong to one narrow concern.
- Do not split code mechanically. If a small private helper is only used by one method and keeping it nearby improves readability, it may remain in the same file.
- Prefer a small number of cohesive files over aggressive fragmentation. A reader should be able to open the primary file for the main type and then find supporting utilities in files named after their domain.

For example, in a package like `executor`, keep `Executor` and its methods in `executor.go`, and move package-level helpers such as runtime context loading or handoff utilities into focused companion files when the file grows large enough that the split improves readability.
