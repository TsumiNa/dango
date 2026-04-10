// Package cli wires command-line parsing to the orchestrator and executor
// services.
//
// Architecturally, this package is the boundary between the operating-system
// process and the reusable dango services. [App] owns Cobra root-command
// construction, shared command output streams, logging bootstrap, and the
// common orchestrator bootstrap path that opens the data directory and SQLite
// store. Package main calls [New] and then enters the package through
// [App.Run], while individual subcommands fan out into orchestrator and
// executor command handlers.
//
// The typical workflow is: construct [App], call [App.Run], let the Cobra root
// command route into either the orchestrator subtree or the executor subtree,
// then let the selected command build any shared dependencies it needs. The
// orchestrator path uses datadir.Locator, logging.Config, and sqlite.Open to
// prepare durable state before it constructs services from the
// orchestrator, runner, and runtime packages. The executor path stays
// in-process and delegates directly to the executor package.
//
// This package deliberately owns CLI composition rather than domain logic. It
// should explain how commands are entered and wired together, but it should not
// become the place where planning, scheduling, registry mutation, or tool
// execution behavior is implemented.
package cli
