package cli

import (
	"context"
	"io"
	"log/slog"

	"github.com/spf13/cobra"
	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/store/sqlite"
)

// App coordinates top-level CLI dispatch for the dango binary.
//
// App is intended to be created once per process. Its zero value is not usable
// because the standard output and error writers must be supplied explicitly.
type App struct {
	stdout io.Writer
	stderr io.Writer
}

// New constructs an [App] that writes command output to stdout and diagnostics
// to stderr.
//
// Callers should provide non-nil writers.
func New(stdout, stderr io.Writer) *App {
	return &App{
		stdout: stdout,
		stderr: stderr,
	}
}

// Run dispatches args to the requested dango mode and subcommand.
//
// Run returns an error for unknown commands, argument parsing failures, and
// command execution failures.
func (a *App) Run(ctx context.Context, args []string) error {
	root := a.newRootCommand()
	root.SetArgs(args)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	return root.ExecuteContext(ctx)
}

func (a *App) newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "dango",
		Short:         "Run dango orchestrator and executor commands",
		SilenceErrors: true,
		SilenceUsage:  true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	root.AddCommand(
		a.newOrchestratorCommand(),
		a.newExecutorCommand(),
	)

	return root
}

func (a *App) newOrchestratorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "orchestrator",
		Short: "Run orchestrator services and administration commands",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(
		a.newOrchestratorServeCommand(),
		a.newOrchestratorRegisterCommand(),
		a.newOrchestratorUnregisterCommand(),
		a.newOrchestratorListToolsCommand(),
	)

	return cmd
}

func (a *App) newExecutorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "executor",
		Short: "Run executor container entrypoints",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(
		a.newExecutorDescribeCommand(),
		a.newExecutorPlanCommand(),
		a.newExecutorRunCommand(),
	)

	return cmd
}

func (a *App) bootstrapOrchestrator(dataDir string) (*datadir.Locator, *sqlite.Store, error) {
	locator, err := datadir.New(dataDir)
	if err != nil {
		return nil, nil, err
	}
	if err := locator.Ensure(); err != nil {
		return nil, nil, err
	}

	store, err := sqlite.Open(locator.DBPath())
	if err != nil {
		return nil, nil, err
	}

	return locator, store, nil
}

func (a *App) newLogger(command string, cfg logging.Config) (*slog.Logger, func(), error) {
	logger, closer, err := logging.New(cfg, a.stderr)
	if err != nil {
		return nil, nil, err
	}

	cleanup := func() {
		if closer != nil {
			_ = closer.Close()
		}
	}

	return logger.With("command", command), cleanup, nil
}
