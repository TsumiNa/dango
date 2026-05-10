// Package persistence defines runner persistence backends and workspace path
// conventions.
//
// PathRule maps a runner ID to a single relative path element under the global
// workspace root. Backend implementations provide orchestrator event-log,
// runner-record, and cursor stores plus the global workspace root used by
// runners. Durable backends currently include SQLite and Postgres.
package persistence
