---
description: "Use when creating, editing, or reviewing SQLite schema changes, migrations, sqlc query definitions, sqlc.yaml, or justfile database recipes. Follow the repository database workflow: write migrations by hand, keep schema.sql in sync for sqlc, regenerate wrappers, and use the justfile database commands."
applyTo: "{internal/store/sqlite/**,sqlc.yaml,justfile}"
---

# Database Workflow

Treat database changes in this repository as an explicit, reviewable workflow.

- Use hand-written migrations as the runtime source of truth. Do not assume schema diffs are converted into migrations automatically.
- Keep `internal/store/sqlite/migrations/` authoritative for database evolution at startup. Each schema change should be introduced as a numbered `.up.sql` and `.down.sql` pair.
- Keep `internal/store/sqlite/schema.sql` aligned with the latest post-migration schema. This file exists primarily as the current schema snapshot for `sqlc`, not as an input for automatic migration generation.
- Keep `internal/store/sqlite/queries.sql` aligned with any schema or query-shape changes that affect reads or writes.
- Regenerate generated query wrappers after editing `schema.sql` or `queries.sql` by running `just db-generate`.
- Preserve the package boundary in `internal/store/sqlite/`: generated code lives in `internal/store/sqlite/db`, while the public store package keeps the stable package-facing API and wraps generated types as needed.

## Required Flow For Schema Changes

When changing a table, column, index, or constraint:

1. Create a new migration pair with `just db-new-migration <name>`.
2. Write the `up` and `down` SQL by hand.
3. Update `internal/store/sqlite/schema.sql` so it reflects the new steady-state schema.
4. Update `internal/store/sqlite/queries.sql` if the application reads or writes the changed structure.
5. Run `just db-generate` to refresh generated wrappers.
6. Run `go test ./...` before considering the change complete.

## SQLite Expectations

- Prefer transparent SQL over implicit ORM-style schema mutation.
- For SQLite, expect some rollback or reshape operations to require table recreation, data copy, drop, and rename sequences rather than single-statement reversals.
- Review the exact SQL that will run in production. If an automatically suggested SQL plan is unclear or risky, rewrite it explicitly.

## justfile Commands

Use the database recipes in `justfile` rather than ad hoc command variants when they cover the task:

- `just db-generate` regenerates `sqlc` wrappers.
- `just db-new-migration <name>` creates the next migration pair.
- `just db-open [data_dir]` opens the SQLite database shell.
- `just db-query <sql> [data_dir]` runs one-off SQL for inspection.

When editing `justfile`, preserve these recipes unless the workflow itself is intentionally changing.
