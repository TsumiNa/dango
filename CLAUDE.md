# Claude Code Instructions

The repository-specific coding rules for this project live in `.github/instructions/`.

Before planning, editing, refactoring, or reviewing code, read the instruction files in `.github/instructions/` that are relevant to the current task and follow them as project policy.

Current instruction files:

- `.github/instructions/database-workflow.instructions.md`
- `.github/instructions/go-file-organization.instructions.md`
- `.github/instructions/go-docs.instructions.md`
- `.github/instructions/in-branch-api-compat.instructions.md`
- `.github/instructions/repository-doc-boundaries.instructions.md`

Working rules:

- Treat `.github/instructions/` as the canonical source of repository-specific instructions.
- Prefer applicable rules in `.github/instructions/` over generic guidance in this file when both address the same topic.
- When a task touches Go code, review the Go instruction files before making changes.
- When refactoring or evolving in-progress APIs within the same branch, consult the in-branch API compatibility instruction before adding wrappers, aliases, or adapter layers.
- When new instruction files are added under `.github/instructions/`, apply them whenever their scope matches the task.
- If two rules appear to conflict, prefer the more specific rule for the file type or task.
- If this file is edited in the future, preserve the rule that `.github/instructions/` must be consulted first and remains the canonical rule set for the repository.

Keep this file as a lightweight entrypoint that points Claude Code to the canonical rules rather than copying those rules here.