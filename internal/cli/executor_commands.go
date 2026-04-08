package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/tsumina/dango/internal/executor"
	"github.com/tsumina/dango/internal/logging"
)

func (a *App) runExecutor(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("executor subcommand is required")
	}

	switch args[0] {
	case "describe":
		return a.runExecutorDescribe(args[1:])
	case "run":
		return a.runExecutorRun(ctx, args[1:])
	default:
		return fmt.Errorf("unknown executor subcommand %q", args[0])
	}
}

func (a *App) runExecutorDescribe(args []string) error {
	fs := flag.NewFlagSet("dango executor describe", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	logCfg := logging.DefaultConfig()
	logCfg.BindFlags(fs)
	format := fs.String("format", "yaml", "output format: yaml|json")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger, cleanup, err := a.newLogger("executor.describe", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	return executor.New(a.stdout, a.stderr, logger).Describe(*format)
}

func (a *App) runExecutorRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dango executor run", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	logCfg := logging.DefaultConfig()
	logCfg.BindFlags(fs)
	taskID := fs.String("task-id", "", "task UUID")
	subTask := fs.String("sub-task", "", "path to sub-task.md")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger, cleanup, err := a.newLogger("executor.run", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	return executor.New(a.stdout, a.stderr, logger).Run(ctx, executor.RunOptions{
		TaskID:  *taskID,
		SubTask: *subTask,
	})
}
