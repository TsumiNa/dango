// Package sqlite implements the SQLite-backed durable state store for dango.
//
// The package owns schema migrations plus the row-level persistence API used by
// the orchestrator and runner. [Open] creates or opens the database file,
// applies embedded migrations, and returns a shared [Store]. [Store] exposes
// CRUD-style methods for the tool registry, tasks, and edges, while
// [StreamStore] persists request event logs, [RunnerStore] persists append-only
// runner checkpoint records, and [SnapshotCursorStore] persists describe replay
// cursors. The row mirror types [ToolRecord], [TaskRecord], and [EdgeRecord]
// describe the stable data shape consumed by callers.
//
// Query implementations come from sqlc-generated wrappers under the db
// subpackage so the repository can keep explicit SQL in version-controlled
// files while avoiding repetitive row-scanning code.
//
// This package deliberately stops at durable relational state. It does not own
// the higher-level workflow semantics of task planning or edge execution, and
// it does not replace filesystem artifacts such as task markdown, metadata
// files, or handoffs.
package sqlite
