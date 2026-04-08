---
description: "Use when creating, editing, or reorganizing repository documentation, especially README.md or CONTRIBUTING.md. Keep README focused on user-facing purpose, usage, and examples, and move developer-facing structure, architecture, and workflow guidance into CONTRIBUTING.md."
applyTo: "**/{README,CONTRIBUTING}.md"
---

# Repository Documentation Boundaries

Keep `README.md` clean and user-facing. Use it to explain what the project or package does, how to use it, and how to get started quickly.

- Prefer `README.md` for project purpose, intended audience, installation, quick start, CLI or API usage, and concise examples.
- Keep `README.md` focused on information a user or evaluator needs first. If it grows too large, move developer-facing material out and leave a short link.
- Do not overload `README.md` with internal package layout, project structure walkthroughs, architecture rationale, implementation notes, coding standards, or development process details.
- Put developer-facing content in `CONTRIBUTING.md`, including project structure, architecture overview, development workflow, testing instructions, coding conventions, documentation conventions, and contribution expectations.
- If contributor-oriented guidance becomes substantial and `CONTRIBUTING.md` does not exist yet, create it rather than expanding `README.md` further.
- Keep README and CONTRIBUTING complementary: `README.md` is the user-facing entrypoint, while `CONTRIBUTING.md` is the developer-facing guide.