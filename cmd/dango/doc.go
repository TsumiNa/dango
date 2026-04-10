// Package main provides the process entrypoint for the dango binary.
//
// The package is intentionally thin. Its only job is to construct the top-level
// CLI application, hand it process stdio, and delegate command dispatch to the
// reusable packages under internal/. The executable exposes two top-level
// workflows: orchestrator commands, which bootstrap the control plane and the
// runner-backed execution services, and executor commands, which expose the
// in-tool describe, plan, and run entrypoints used by registered tools.
//
// The runtime call path is therefore small and stable: main constructs a
// cli.App, passes os.Args to cli.App.Run, and reports any terminal error to
// stderr before exiting non-zero. All orchestration, persistence, planning,
// scheduling, prompt rendering, and runtime execution logic lives outside this
// package so that package main remains a bootstrap layer rather than a second
// copy of the application's business logic.
package main
