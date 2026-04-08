package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/orchestrator"
	"github.com/tsumina/dango/internal/runtime"
)

func (a *App) runOrchestratorServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dango orchestrator serve", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	logCfg := logging.DefaultConfig()
	logCfg.BindFlags(fs)
	model := fs.String("model", "gemini-2.5-pro", "AI model for orchestration")
	port := fs.Int("port", 8080, "listen port")
	dataDir := fs.String("data-dir", "/data", "root data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger, cleanup, err := a.newLogger("orchestrator.serve", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	layout, store, err := a.bootstrapOrchestrator(*dataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	logger.Info("starting orchestrator server", "model", *model, "port", *port, "data_dir", layout.Root)
	rt := runtime.NewDefault(os.Getenv("DANGO_DOCKER_BIN"), logger)
	registry := orchestrator.NewRegistryService(layout, store, rt, logger)
	taskService := orchestrator.NewTaskService(layout, store, logger)
	planner := orchestrator.NewPlanner(store, logger)
	scheduler := orchestrator.NewScheduler(layout, store, rt, logger)
	engine := orchestrator.NewDemoEngine(layout, store, taskService, planner, scheduler, logger)
	server := orchestrator.NewServer(":"+strconv.Itoa(*port), registry, taskService, engine, logger)

	serverCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return server.ListenAndServe(serverCtx)
}

func (a *App) runOrchestratorRegister(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dango orchestrator register", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	logCfg := logging.DefaultConfig()
	logCfg.BindFlags(fs)
	override := fs.String("override", "", "path to override.yaml")
	dataDir := fs.String("data-dir", "/data", "root data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: dango orchestrator register <image:tag> [--override path]")
	}

	layout, store, err := a.bootstrapOrchestrator(*dataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	logger, cleanup, err := a.newLogger("orchestrator.register", logCfg)
	if err != nil {
		return err
	}
	defer cleanup()

	imageRef, err := runtime.NormalizeImageReference(fs.Arg(0))
	if err != nil {
		return err
	}

	logger.Info("register command started", "image", imageRef, "override_path", *override, "data_dir", layout.Root)
	service := orchestrator.NewRegistryService(layout, store, runtime.NewDefault(os.Getenv("DANGO_DOCKER_BIN"), logger), logger)
	tool, err := service.Register(ctx, imageRef, *override)
	if err != nil {
		return err
	}

	return json.NewEncoder(a.stdout).Encode(tool)
}

func (a *App) runOrchestratorUnregister(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dango orchestrator unregister", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	logCfg := logging.DefaultConfig()
	logCfg.BindFlags(fs)
	dataDir := fs.String("data-dir", "/data", "root data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: dango orchestrator unregister <tool_name>")
	}

	layout, store, err := a.bootstrapOrchestrator(*dataDir)
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
	if err := service.Unregister(ctx, fs.Arg(0)); err != nil {
		return err
	}

	_, err = fmt.Fprintf(a.stdout, "unregistered %s\n", fs.Arg(0))
	return err
}

func (a *App) runOrchestratorListTools(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dango orchestrator list-tools", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	logCfg := logging.DefaultConfig()
	logCfg.BindFlags(fs)
	dataDir := fs.String("data-dir", "/data", "root data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	layout, store, err := a.bootstrapOrchestrator(*dataDir)
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

func (a *App) runOrchestratorDemoRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dango orchestrator demo-run", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	logCfg := logging.DefaultConfig()
	logCfg.BindFlags(fs)
	request := fs.String("request", "", "user request for the demo flow")
	dataDir := fs.String("data-dir", filepath.Join(".", ".dango-demo"), "root data directory")
	toolsDir := fs.String("tools-dir", "", "directory containing external demo tools; if empty, built-in toy tools are materialized under data-dir")
	if err := fs.Parse(args); err != nil {
		return err
	}

	requestText := strings.TrimSpace(*request)
	if requestText == "" && fs.NArg() > 0 {
		requestText = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
	if requestText == "" {
		return fmt.Errorf("demo request is required via --request or positional args")
	}

	layout, store, err := a.bootstrapOrchestrator(*dataDir)
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
	resolvedToolsDir := *toolsDir
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
