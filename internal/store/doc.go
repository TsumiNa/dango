// Package store defines application-facing persistence abstractions.
//
// This package owns the storage contracts used by higher-level runtime code
// without binding that code to a concrete backend. It includes event-log and
// snapshot-cursor abstractions for request-stream replay in addition to the
// runner checkpoint and row-oriented stores implemented by backend packages.
// Backend-specific implementations live in sibling packages such as
// internal/store/sqlite.
package store
