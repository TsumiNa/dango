// Package cli wires command-line parsing to orchestrator and executor services.
//
// The package owns top-level mode dispatch, subcommand parsing, and common
// process concerns such as logging setup and orchestrator bootstrap. The main
// entrypoint is [App], created by [New], with command execution handled by
// [App.Run].
package cli
