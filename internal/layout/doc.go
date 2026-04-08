// Package layout resolves the orchestrator data directory contract.
//
// It centralizes path construction for registry artifacts, per-task files,
// edge directories, and SQLite storage. Using [Layout] avoids duplicating path
// rules across services and keeps file locations stable for both runtime code
// and tests.
package layout
