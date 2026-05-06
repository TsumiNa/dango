---
description: "Use when starting work on a change, bug fix, refactor, upgrade, or any modification request. Covers when to create a new branch, when to stay on the current branch, and when to open a pull request before continuing."
name: "Branch and Pull Request Workflow"
applyTo: "**"
---

# Branch and Pull Request Workflow

When the user asks for a code change, feature, fix, refactor, upgrade, or any modification, decide where to do the work by checking the current branch state **before** editing files.

## Decision Order

In the Decision Order section, check these conditions in order and stop at the first match.

| Order | Condition | Action |
| --- | --- | --- |
| 1 | The user explicitly opts out of a new branch or PR, for example "just edit on main", "no PR needed", "直接改", or "不用开 PR". | Honor the user's direction and work wherever they indicate. |
| 2 | The current branch already has an open PR. | Continue working on the current branch. Do not create a new branch. Push follow-up commits to the same branch so they land on the existing PR. |
| 3 | The current branch is ahead of the default branch but has no PR yet. | Open a PR for the current branch against the default branch first, then continue working on the same branch. New commits will land on the newly opened PR. |
| 4 | The current branch is the default branch, is in sync with it, or none of the above conditions apply. | Create a new branch off the default branch, make the change there, and open a PR when the change is ready to share. |

## Practical Rules

- Determine the current branch and its PR status before making code changes, not after. Use the repository's PR metadata (for example `currentActivePullRequest`) or `git` commands to check.
- Name new branches descriptively for the change (for example `feat/...`, `fix/...`, `docs/...`, `refactor/...`).
- Do not commit directly to the default branch unless case 1 applies.
- When case 3 applies, do not silently keep committing without a PR; open the PR first so the work is reviewable.
- When case 2 applies, do not open a second PR for the same branch.
- If it is unclear whether an existing branch is "ahead but unpushed" versus "already has a PR", prefer checking remote state before deciding.

## Anti-patterns

- Creating a new branch when the user is already on a branch with an open PR for the same piece of work.
- Committing to the default branch for a non-trivial change without asking.
- Pushing follow-up commits to a feature branch that is ahead of main without ever opening a PR for it.
- Opening a new PR for in-progress follow-up work that belongs on an already-open PR.
