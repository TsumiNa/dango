package cli

import (
	"context"

	"github.com/spf13/cobra"
	"github.com/tsumina/dango/internal/executor"
	"github.com/tsumina/dango/internal/logging"
)

func (a *App) newExecutorDescribeCommand() *cobra.Command {
	logCfg := logging.DefaultConfig()
	format := "yaml"

	cmd := &cobra.Command{
		Use:   "describe",
		Short: "Print the local tool specification",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runExecutorDescribe(logCfg, format)
		},
	}

	flags := cmd.Flags()
	logCfg.BindFlags(flags)
	flags.StringVar(&format, "format", format, "output format: yaml|json")

	return cmd
}

func (a *App) runExecutorDescribe(logCfg logging.Config, format string) error {
	logger, cleanup, err := a.newLogger("executor.describe", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	return executor.New(a.stdout, a.stderr, logger).Describe(format)
}

func (a *App) newExecutorRunCommand() *cobra.Command {
	logCfg := logging.DefaultConfig()
	var taskID string
	var subTask string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute a tool task using the scheduler runtime contract",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runExecutorRun(cmd.Context(), logCfg, taskID, subTask)
		},
	}

	flags := cmd.Flags()
	logCfg.BindFlags(flags)
	flags.StringVar(&taskID, "task-id", "", "task UUID")
	flags.StringVar(&subTask, "sub-task", "", "path to sub-task.md")

	return cmd
}

func (a *App) runExecutorRun(ctx context.Context, logCfg logging.Config, taskID string, subTask string) error {
	logger, cleanup, err := a.newLogger("executor.run", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	return executor.New(a.stdout, a.stderr, logger).Run(ctx, executor.RunOptions{
		TaskID:  taskID,
		SubTask: subTask,
	})
}

func (a *App) newExecutorPlanCommand() *cobra.Command {
	logCfg := logging.DefaultConfig()
	var taskID string
	var subTask string
	format := "json"

	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Refine a planned tool stage and emit a structured plan",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runExecutorPlan(cmd.Context(), logCfg, taskID, subTask, format)
		},
	}

	flags := cmd.Flags()
	logCfg.BindFlags(flags)
	flags.StringVar(&taskID, "task-id", "", "task UUID")
	flags.StringVar(&subTask, "sub-task", "", "path to sub-task.md")
	flags.StringVar(&format, "format", format, "output format: json|yaml")

	return cmd
}

func (a *App) runExecutorPlan(ctx context.Context, logCfg logging.Config, taskID string, subTask string, format string) error {
	logger, cleanup, err := a.newLogger("executor.plan", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	return executor.New(a.stdout, a.stderr, logger).Plan(ctx, executor.PlanOptions{
		TaskID:  taskID,
		SubTask: subTask,
		Format:  format,
	})
}
