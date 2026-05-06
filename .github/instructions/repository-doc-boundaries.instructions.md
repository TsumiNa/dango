---
description: "Use when creating, editing, or reorganizing repository documentation, especially README.md or CONTRIBUTING.md. Keep README focused on user-facing purpose, usage, and examples, and move developer-facing structure, architecture, and workflow guidance into CONTRIBUTING.md."
applyTo: "**/{README,CONTRIBUTING}.md"
---

# Repository Documentation Boundaries

Keep `README.md` user-facing and keep `CONTRIBUTING.md` developer-facing.

- Step 1: Put user-first content in `README.md` (purpose, audience, install, quick start, CLI/API usage, concise examples).
- Step 2: Put contributor workflow content in `CONTRIBUTING.md` (project structure, architecture, development/testing workflow, coding and documentation conventions, contribution expectations).
- Step 3: If `README.md` starts accumulating contributor guidance, move that material to `CONTRIBUTING.md` and leave a short link in `README.md`.