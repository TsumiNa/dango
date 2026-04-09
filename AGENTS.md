# Agent Instructions

This repository keeps its canonical agent rules in `.github/instructions/`.

Before planning, editing, refactoring, or reviewing code, read the instruction files in `.github/instructions/` that are relevant to the task at hand and follow them as repository policy.

Current instruction files:

- `.github/instructions/database-workflow.instructions.md`
- `.github/instructions/go-file-organization.instructions.md`
- `.github/instructions/go-docs.instructions.md`
- `.github/instructions/repository-doc-boundaries.instructions.md`

Working rules:

- Treat `.github/instructions/` as the source of truth for repository-specific coding rules.
- Prefer applicable rules in `.github/instructions/` over generic guidance in this file when both address the same topic.
- When a task touches Go code, review the Go instruction files before making changes.
- When new instruction files are added under `.github/instructions/`, treat them as applicable repository guidance whenever their scope matches the task.
- If two rules appear to conflict, prefer the more specific rule for the file type or task.
- If this file is edited in the future, preserve the rule that `.github/instructions/` must be consulted first and remains the canonical rule set for the repository.

Do not duplicate the detailed rules here unless the repository intentionally decides to move the source of truth.