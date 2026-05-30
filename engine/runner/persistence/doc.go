// Package persistence defines the runner persistence backend contract.
//
// Backend implementations provide the orchestrator event-log store, the
// runner-record store, and the describe-replay cursor store, plus the global
// workspace root that runners use to provision their per-runner subdirectories.
// Durable backends currently include SQLite and Postgres; a markdown-mirror
// backend is provided for human-readable inspection.
package persistence
