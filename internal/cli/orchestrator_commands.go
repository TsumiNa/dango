package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/orchestrator"
	"github.com/tsumina/dango/internal/runner"
	"github.com/tsumina/dango/internal/runner/runtime"
)

func (a *App) newOrchestratorServeCommand() *cobra.Command {
	logCfg := logging.DefaultConfig()
	model := "gemini-3.5-pro"
	port := 8080
	dataDir := defaultDataDir()
	unixSocket := filepath.Join(dataDir, "orchestrator.sock")

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the orchestrator API server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOrchestratorServe(cmd.Context(), logCfg, model, port, dataDir, unixSocket)
		},
	}

	flags := cmd.Flags()
	logCfg.BindFlags(flags)
	flags.StringVar(&model, "model", model, "AI model for orchestration")
	flags.IntVar(&port, "port", port, "listen port")
	flags.StringVar(&unixSocket, "unix-socket", unixSocket, "path to the Unix domain socket listener")
	flags.StringVar(&dataDir, "data-dir", dataDir, "root data directory")

	return cmd
}

func (a *App) runOrchestratorServe(ctx context.Context, logCfg logging.Config, model string, port int, dataDir string, unixSocket string) error {
	logger, cleanup, err := a.newLogger("orchestrator.serve", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	locator, store, err := a.bootstrapOrchestrator(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	logger.Info("starting orchestrator server", "model", model, "port", port, "data_dir", locator.Root)
	rt := runtime.NewDefault(os.Getenv("DANGO_DOCKER_BIN"), logger)
	registry := orchestrator.NewRegistryService(locator, store, rt, logger)
	taskService := orchestrator.NewTaskService(locator, store, logger)
	llmClient := llm.NewOpenAICompatibleFromEnv(model, logger)
	planner := runner.NewPlanner(locator, store, rt, llmClient, logger)
	scheduler := runner.NewScheduler(locator, store, rt, logger)
	runners := runner.NewTaskRunnerService(locator, taskService, planner, scheduler, logger)
	server := orchestrator.NewServer(orchestrator.ServerConfig{
		TCPAddress:     ":" + strconv.Itoa(port),
		UnixSocketPath: unixSocket,
	}, registry, taskService, runners, llmClient, logger)

	serverCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return server.ListenAndServe(serverCtx)
}

func (a *App) newOrchestratorRegisterCommand() *cobra.Command {
	logCfg := logging.DefaultConfig()
	var override string
	dataDir := defaultDataDir()

	cmd := &cobra.Command{
		Use:   "register <image:tag>",
		Short: "Register a tool image with the orchestrator",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOrchestratorRegister(cmd.Context(), logCfg, args[0], override, dataDir)
		},
	}

	flags := cmd.Flags()
	logCfg.BindFlags(flags)
	flags.StringVar(&override, "override", "", "path to override.yaml")
	flags.StringVar(&dataDir, "data-dir", dataDir, "root data directory")

	return cmd
}

func (a *App) runOrchestratorRegister(ctx context.Context, logCfg logging.Config, imageArg string, override string, dataDir string) error {
	locator, store, err := a.bootstrapOrchestrator(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	logger, cleanup, err := a.newLogger("orchestrator.register", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	imageRef, err := runtime.NormalizeImageReference(imageArg)
	if err != nil {
		return err
	}

	logger.Info("register command started", "image", imageRef, "override_path", override, "data_dir", locator.Root)
	service := orchestrator.NewRegistryService(locator, store, runtime.NewDefault(os.Getenv("DANGO_DOCKER_BIN"), logger), logger)
	tool, err := service.Register(ctx, imageRef, override)
	if err != nil {
		return err
	}

	return json.NewEncoder(a.stdout).Encode(tool)
}

func (a *App) newOrchestratorUnregisterCommand() *cobra.Command {
	logCfg := logging.DefaultConfig()
	dataDir := defaultDataDir()

	cmd := &cobra.Command{
		Use:   "unregister <tool-name>",
		Short: "Unregister a tool from the orchestrator",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOrchestratorUnregister(cmd.Context(), logCfg, args[0], dataDir)
		},
	}

	flags := cmd.Flags()
	logCfg.BindFlags(flags)
	flags.StringVar(&dataDir, "data-dir", dataDir, "root data directory")

	return cmd
}

func (a *App) runOrchestratorUnregister(ctx context.Context, logCfg logging.Config, toolName string, dataDir string) error {
	locator, store, err := a.bootstrapOrchestrator(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	logger, cleanup, err := a.newLogger("orchestrator.unregister", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	service := orchestrator.NewRegistryService(locator, store, runtime.NewDefault(os.Getenv("DANGO_DOCKER_BIN"), logger), logger)
	if err := service.Unregister(ctx, toolName); err != nil {
		return err
	}

	_, err = fmt.Fprintf(a.stdout, "unregistered %s\n", toolName)
	return err
}

func (a *App) newOrchestratorListToolsCommand() *cobra.Command {
	logCfg := logging.DefaultConfig()
	dataDir := defaultDataDir()

	cmd := &cobra.Command{
		Use:   "list-tools",
		Short: "List registered tools",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOrchestratorListTools(cmd.Context(), logCfg, dataDir)
		},
	}

	flags := cmd.Flags()
	logCfg.BindFlags(flags)
	flags.StringVar(&dataDir, "data-dir", dataDir, "root data directory")

	return cmd
}

func (a *App) runOrchestratorListTools(ctx context.Context, logCfg logging.Config, dataDir string) error {
	locator, store, err := a.bootstrapOrchestrator(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	logger, cleanup, err := a.newLogger("orchestrator.list-tools", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	service := orchestrator.NewRegistryService(locator, store, runtime.NewDefault(os.Getenv("DANGO_DOCKER_BIN"), logger), logger)
	tools, err := service.List(ctx)
	if err != nil {
		return err
	}

	return json.NewEncoder(a.stdout).Encode(map[string]any{"tools": tools})
}

func defaultDataDir() string {
	root, err := datadir.DefaultRoot()
	if err == nil {
		return root
	}

	// Fall back to a relative path when the user home directory cannot be
	// resolved during command construction.
	return filepath.Join(".dango", "data")
}
