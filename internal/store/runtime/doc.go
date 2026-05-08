// Package runtime opens the startup-owned persistence bundle used by dango.
//
// The package selects either a process-lifetime temporary JSON fallback or a
// configured SQLite backend, then exposes the request event-log store, runner
// checkpoint store, and snapshot cursor store that higher-level services wire
// into the orchestrator.
package runtime