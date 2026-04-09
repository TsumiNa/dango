// Package sqlite implements the SQLite-backed orchestration state store.
//
// The package owns versioned schema migrations and CRUD operations for tools,
// tasks, edges, and scheduler logs. [Open] applies embedded migrations
// automatically and returns [Store], which is then shared by orchestrator
// services. Query methods are implemented with sqlc-generated database/sql
// wrappers so the package keeps explicit SQL while reducing scan boilerplate.
package sqlite
