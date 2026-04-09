// Package datadir resolves canonical paths within the orchestrator data
// directory.
//
// It centralizes path construction for registry artifacts, per-task files,
// edge directories, and SQLite storage. [Locator] is the main entrypoint, and
// [AppHome] and [DefaultRoot] define the default user-scoped location under
// `~/.dango`. The package keeps path rules stable across runtime code and tests
// and avoids duplicating directory conventions throughout the orchestrator.
package datadir
