// Package sqlite implements the SQLite-backed durable state store for dango.
//
// The package owns schema migrations plus the row-level persistence API used by
// the orchestrator and runner. [Open] creates or opens the database file,
// applies embedded migrations, and returns a shared [Store]. [Store] then
// exposes CRUD-style methods for the tool registry, tasks, and edges, while
// [StreamStore] persists request event logs, [RunnerStore] persists append-only
// runner checkpoint records, and [SnapshotCursorStore] persists describe replay
// cursors. The row mirror types [ToolRecord], [TaskRecord], and [EdgeRecord]
// provide the stable data shape consumed by the higher-level services.
//
// The typical workflow is: the CLI bootstraps the data directory, calls
// [Open], passes the resulting [Store] into the orchestrator services, and then
// lets those services translate higher-level operations into row mutations.
// [RegistryService] persists [ToolRecord] values, [TaskService] persists and
// reads [TaskRecord] values, and the runner and scheduler persist [EdgeRecord]
// state as execution progresses. Query implementations come from sqlc-generated
// wrappers under the db subpackage so the repository can keep explicit SQL in
// version-controlled files while avoiding repetitive row scanning code.
//
// This package deliberately stops at durable relational state. It does not own
// the higher-level workflow semantics of task planning or edge execution, and it
// does not replace filesystem artifacts such as task markdown, metadata files,
// or handoffs. Instead, it complements the datadir package by keeping the
// normalized row state that those artifact-oriented packages refer to.
package sqlite
