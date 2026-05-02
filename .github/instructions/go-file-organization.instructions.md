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
- Avoid creating tiny production files that contain only one or two small functions or type definitions unless that file is the clear package entry point, package documentation, generated code boundary, or a genuinely independent domain unit. A short helper like `stream_events.go` is usually better kept near the code that emits those events or folded into a broader cohesive file.
- Avoid splitting one concern into several similarly named files such as `xxx_yyy_zzz.go`, `xxx_yyy_ddd.go`, and `xxx_yyy_aaa.go`. Similar filenames increase navigation cost and usually signal that the package has been fragmented around implementation details rather than reader-facing concepts.
- Follow Go filename conventions: keep production filenames short, lowercase, and named for a stable package concept. Outside of conventional suffixes such as `_test.go`, `_unix.go`, `_linux.go`, `_windows.go`, or generated-code markers, prefer zero or one underscore in a production filename. Use multiple underscores only when there is a strong Go toolchain or platform-build reason.
- Before creating a new production file, first ask whether the code belongs in an existing cohesive file. Create the file only when it makes the package easier to scan after considering both file size and file count.

For example, in a package like `executor`, keep `Executor` and its methods in `executor.go`, and move package-level helpers such as runtime context loading or handoff utilities into focused companion files when the file grows large enough that the split improves readability.
