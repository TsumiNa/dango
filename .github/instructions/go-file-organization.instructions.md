---
description: "Use when creating, editing, or refactoring Go files, especially when a file mixes a primary type with standalone helper functions. Prefer keeping a struct with its constructor and methods while moving helpers that serve a separate package responsibility or multiple primary types into focused files."
applyTo: "**/*.go"
---

# Go File Organization

Organize Go code by package responsibility and reader navigation. Apply these priorities in order:

- Keep a primary struct, its constructor, exported API surface, methods, and tightly coupled supporting types in the same file when they form one cohesive unit.
- Keep small private helpers near their only caller when that improves readability.
- Move standalone helpers into focused companion files only when they serve a distinct package responsibility, support multiple primary types, or make an oversized file easier to scan. Examples include runtime context loading, handoff writing, tool spec loading, validation, or conversion.
- Name production files after stable package concepts, such as `runtime_context.go`, `handoff.go`, or `tool_spec.go`. Keep names short and lowercase; outside conventional suffixes such as `_test.go`, `_unix.go`, `_linux.go`, `_windows.go`, or generated-code markers, prefer zero or one underscore unless the Go toolchain or platform builds require more.
- Avoid generic names like `helpers.go` unless the package already uses that pattern and the helpers truly belong to one narrow concern.
- Avoid tiny production files containing only one or two small functions or type definitions unless the file is a clear package entry point, package documentation, generated code boundary, or independent domain unit.
- Avoid splitting one concern into several similarly named files such as `xxx_yyy_zzz.go`, `xxx_yyy_ddd.go`, and `xxx_yyy_aaa.go`; similar filenames increase navigation cost and usually signal fragmentation around implementation details.
- Before creating a new production file, first ask whether the code belongs in an existing cohesive file. Create the file only when it makes the package easier to scan after considering both file size and file count.

For example, in a package like `executor`, keep `Executor` and its methods in `executor.go`, and move package-level helpers such as runtime context loading or handoff utilities into focused companion files when the file grows large enough that the split improves readability.
