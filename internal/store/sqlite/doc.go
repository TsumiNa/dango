// Package sqlite implements the SQLite-backed orchestration state store.
//
// The package owns schema initialization and CRUD operations for tools, tasks,
// edges, and scheduler logs. [Open] applies migrations automatically and
// returns [Store], which is then shared by orchestrator services.
package sqlite
