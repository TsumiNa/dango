package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/orchestrator"
	"github.com/tsumina/dango/internal/runtime"
)

func (a *App) newOrchestratorServeCommand() *cobra.Command {
	logCfg := logging.DefaultConfig()
	model := "gemini-3.5-pro"
	port := 8080
	dataDir := "/data"

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the orchestrator API server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOrchestratorServe(cmd.Context(), logCfg, model, port, dataDir)
		},
	}

	flags := cmd.Flags()
	logCfg.BindFlags(flags)
	flags.StringVar(&model, "model", model, "AI model for orchestration")
	flags.IntVar(&port, "port", port, "listen port")
	flags.StringVar(&dataDir, "data-dir", dataDir, "root data directory")

	return cmd
}

func (a *App) runOrchestratorServe(ctx context.Context, logCfg logging.Config, model string, port int, dataDir string) error {
	logger, cleanup, err := a.newLogger("orchestrator.serve", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	layout, store, err := a.bootstrapOrchestrator(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	logger.Info("starting orchestrator server", "model", model, "port", port, "data_dir", layout.Root)
	rt := runtime.NewDefault(os.Getenv("DANGO_DOCKER_BIN"), logger)
	registry := orchestrator.NewRegistryService(layout, store, rt, logger)
	taskService := orchestrator.NewTaskService(layout, store, logger)
	planner := orchestrator.NewPlanner(store, logger)
	scheduler := orchestrator.NewScheduler(layout, store, rt, logger)
	engine := orchestrator.NewDemoEngine(layout, store, taskService, planner, scheduler, logger)
	server := orchestrator.NewServer(":"+strconv.Itoa(port), registry, taskService, engine, logger)

	serverCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return server.ListenAndServe(serverCtx)
}

func (a *App) newOrchestratorRegisterCommand() *cobra.Command {
	logCfg := logging.DefaultConfig()
	var override string
	dataDir := "/data"

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
	layout, store, err := a.bootstrapOrchestrator(dataDir)
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

	logger.Info("register command started", "image", imageRef, "override_path", override, "data_dir", layout.Root)
	service := orchestrator.NewRegistryService(layout, store, runtime.NewDefault(os.Getenv("DANGO_DOCKER_BIN"), logger), logger)
	tool, err := service.Register(ctx, imageRef, override)
	if err != nil {
		return err
	}

	return json.NewEncoder(a.stdout).Encode(tool)
}

func (a *App) newOrchestratorUnregisterCommand() *cobra.Command {
	logCfg := logging.DefaultConfig()
	dataDir := "/data"

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
	layout, store, err := a.bootstrapOrchestrator(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	logger, cleanup, err := a.newLogger("orchestrator.unregister", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	service := orchestrator.NewRegistryService(layout, store, runtime.NewDefault(os.Getenv("DANGO_DOCKER_BIN"), logger), logger)
	if err := service.Unregister(ctx, toolName); err != nil {
		return err
	}

	_, err = fmt.Fprintf(a.stdout, "unregistered %s\n", toolName)
	return err
}

func (a *App) newOrchestratorListToolsCommand() *cobra.Command {
	logCfg := logging.DefaultConfig()
	dataDir := "/data"

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
	layout, store, err := a.bootstrapOrchestrator(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	logger, cleanup, err := a.newLogger("orchestrator.list-tools", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	service := orchestrator.NewRegistryService(layout, store, runtime.NewDefault(os.Getenv("DANGO_DOCKER_BIN"), logger), logger)
	tools, err := service.List(ctx)
	if err != nil {
		return err
	}

	return json.NewEncoder(a.stdout).Encode(map[string]any{"tools": tools})
}

func (a *App) newOrchestratorDemoRunCommand() *cobra.Command {
	logCfg := logging.DefaultConfig()
	var request string
	dataDir := filepath.Join(".", ".dango-demo")
	var toolsDir string

	cmd := &cobra.Command{
		Use:   "demo-run [request...]",
		Short: "Run the local demo orchestration flow",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runOrchestratorDemoRun(cmd.Context(), logCfg, request, dataDir, toolsDir, args)
		},
	}

	flags := cmd.Flags()
	logCfg.BindFlags(flags)
	flags.StringVar(&request, "request", "", "user request for the demo flow")
	flags.StringVar(&dataDir, "data-dir", dataDir, "root data directory")
	flags.StringVar(&toolsDir, "tools-dir", "", "directory containing external demo tools; if empty, built-in toy tools are materialized under data-dir")

	return cmd
}

func (a *App) runOrchestratorDemoRun(ctx context.Context, logCfg logging.Config, request string, dataDir string, toolsDir string, args []string) error {
	requestText := strings.TrimSpace(request)
	if requestText == "" && len(args) > 0 {
		requestText = strings.TrimSpace(strings.Join(args, " "))
	}
	if requestText == "" {
		return fmt.Errorf("demo request is required via --request or positional args")
	}

	layout, store, err := a.bootstrapOrchestrator(dataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	logger, cleanup, err := a.newLogger("orchestrator.demo-run", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	logger.Info("demo run command started", "data_dir", layout.Root)
	rt := runtime.NewDefault(os.Getenv("DANGO_DOCKER_BIN"), logger)
	resolvedToolsDir := toolsDir
	if strings.TrimSpace(resolvedToolsDir) == "" {
		resolvedToolsDir, err = materializeBuiltinDemoTools(layout.Root, logger)
		if err != nil {
			return err
		}
	}

	registry := orchestrator.NewRegistryService(layout, store, rt, logger)
	if err := ensureDemoToolsRegistered(ctx, registry, resolvedToolsDir, logger); err != nil {
		return err
	}

	taskService := orchestrator.NewTaskService(layout, store, logger)
	planner := orchestrator.NewPlanner(store, logger)
	scheduler := orchestrator.NewScheduler(layout, store, rt, logger)
	engine := orchestrator.NewDemoEngine(layout, store, taskService, planner, scheduler, logger)

	result, err := engine.Run(ctx, requestText)
	if err != nil {
		return err
	}

	return json.NewEncoder(a.stdout).Encode(result)
}
