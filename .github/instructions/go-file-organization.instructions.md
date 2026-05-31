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
- Do not repeat the package name in a file name. The package already qualifies every identifier, so the prefix is noise: in package `agent` name the file `prompt.go`, not `agent_prompt.go`; in package `orchestrator` name it `planning.go`, not `orchestrator_planning.go`. The only idiomatic exception is the file named after the package itself (for example `agent.go` holding the `Agent` type) and its `agent_test.go` companion — keep those.
- Drop redundant suffixes that merely restate what the package or file already conveys. In a package whose domain is exchange documents, prefer `exchange.go` over `exchange_doc.go`. Keep a qualifier only when it distinguishes the file from a sibling (for example `handoff.go` versus `handoff_artifacts.go`).
- Distinguish a redundant prefix from an intentional grouping prefix. A shared prefix that clusters several sibling files around one sub-concept — `conversation_run.go` and `conversation_stream.go`, `tool_call.go` and `tool_config.go`, `cursor_store.go` and `runner_store.go` — aids navigation and should be kept. Treat a prefix as redundant (and strip it) only when it repeats the package name or restates what every file in the package already is.
- Avoid generic names like `helpers.go` unless the package already uses that pattern and the helpers truly belong to one narrow concern.
- Avoid tiny production files containing only one or two small functions or type definitions unless the file is a clear package entry point, package documentation, generated code boundary, or independent domain unit.
- Avoid splitting one concern into several similarly named files such as `xxx_yyy_zzz.go`, `xxx_yyy_ddd.go`, and `xxx_yyy_aaa.go`; similar filenames increase navigation cost and usually signal fragmentation around implementation details.
- Before creating a new production file, first ask whether the code belongs in an existing cohesive file. Create the file only when it makes the package easier to scan after considering both file size and file count.

For example, in a package like `agent`, keep `Agent` and its methods in `agent.go`, and move package-level helpers such as prompt building or stage execution into focused companion files named `prompt.go` and `stage.go` — not `agent_prompt.go` or `agent_stage.go` — when the file grows large enough that the split improves readability.
