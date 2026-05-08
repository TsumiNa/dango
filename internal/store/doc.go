// Package store defines application-facing persistence abstractions.
//
// This package owns the storage contracts used by higher-level runtime code
// without binding that code to a concrete backend. Backend-specific
// implementations live in sibling packages such as internal/store/sqlite.
package store
