package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/tsumina/dango/internal/layout"
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

// New constructs an App that writes command output to stdout and diagnostics to
// stderr.
func New(stdout, stderr io.Writer) *App {
	return &App{
		stdout: stdout,
		stderr: stderr,
	}
}

// Run dispatches the provided CLI arguments to the requested dango mode.
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return a.usage()
	}

	switch args[0] {
	case "orchestrator":
		return a.runOrchestrator(ctx, args[1:])
	case "executor":
		return a.runExecutor(ctx, args[1:])
	default:
		return fmt.Errorf("unknown mode %q", args[0])
	}
}

func (a *App) runOrchestrator(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("orchestrator subcommand is required")
	}

	switch args[0] {
	case "serve":
		return a.runOrchestratorServe(ctx, args[1:])
	case "register":
		return a.runOrchestratorRegister(ctx, args[1:])
	case "unregister":
		return a.runOrchestratorUnregister(ctx, args[1:])
	case "list-tools":
		return a.runOrchestratorListTools(ctx, args[1:])
	case "demo-run":
		return a.runOrchestratorDemoRun(ctx, args[1:])
	default:
		return fmt.Errorf("unknown orchestrator subcommand %q", args[0])
	}
}

func (a *App) bootstrapOrchestrator(dataDir string) (*layout.Layout, *sqlite.Store, error) {
	layout, err := layout.New(dataDir)
	if err != nil {
		return nil, nil, err
	}
	if err := layout.Ensure(); err != nil {
		return nil, nil, err
	}

	store, err := sqlite.Open(layout.DBPath())
	if err != nil {
		return nil, nil, err
	}

	return layout, store, nil
}

func (a *App) usage() error {
	_, _ = fmt.Fprintln(a.stderr, `usage:
  dango orchestrator serve [--model gemini-2.5-pro] [--port 8080] [--data-dir /data] [--log-level info] [--log-format text]
  dango orchestrator register <image:tag> [--override path] [--data-dir /data] [--log-level info]
  dango orchestrator unregister <tool_name> [--data-dir /data] [--log-level info]
  dango orchestrator list-tools [--data-dir /data] [--log-level info]
  dango orchestrator demo-run --request "draft a demo report" [--data-dir ./.dango-demo] [--tools-dir /path/to/tools] [--log-level debug]

  dango executor describe [--format yaml|json] [--log-level info]
  dango executor run --task-id <uuid> [--sub-task path] [--log-level info]`)
	return fmt.Errorf("missing command")
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
