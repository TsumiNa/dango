package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/tsumina/dango/internal/executor"
	"github.com/tsumina/dango/internal/layout"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/orchestrator"
	"github.com/tsumina/dango/internal/runtime"
	"github.com/tsumina/dango/internal/store/sqlite"
)

type App struct {
	stdout io.Writer
	stderr io.Writer
}

func New(stdout, stderr io.Writer) *App {
	return &App{
		stdout: stdout,
		stderr: stderr,
	}
}

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

func (a *App) runExecutor(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("executor subcommand is required")
	}

	switch args[0] {
	case "describe":
		fs := flag.NewFlagSet("dango executor describe", flag.ContinueOnError)
		fs.SetOutput(a.stderr)
		logCfg := logging.DefaultConfig()
		logCfg.BindFlags(fs)
		format := fs.String("format", "yaml", "output format: yaml|json")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		logger, cleanup, err := a.newLogger("executor.describe", logCfg)
		if err != nil {
			return err
		}
		defer cleanup()
		return executor.New(a.stdout, a.stderr, logger).Describe(*format)
	case "run":
		fs := flag.NewFlagSet("dango executor run", flag.ContinueOnError)
		fs.SetOutput(a.stderr)
		logCfg := logging.DefaultConfig()
		logCfg.BindFlags(fs)
		taskID := fs.String("task-id", "", "task UUID")
		subTask := fs.String("sub-task", "", "path to sub-task.md")
		if err := fs.Parse(args[1:]); err != nil {
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
	default:
		return fmt.Errorf("unknown executor subcommand %q", args[0])
	}
}

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

func ensureDemoToolsRegistered(ctx context.Context, registry *orchestrator.RegistryService, toolsDir string, logger *slog.Logger) error {
	absoluteToolsDir, err := filepath.Abs(toolsDir)
	if err != nil {
		return fmt.Errorf("resolve demo tools dir %q: %w", toolsDir, err)
	}
	logger = logging.Component(logger, "cli.demo-tools")
	logger.Info("registering demo tools", "tools_dir", absoluteToolsDir)

	entries, err := os.ReadDir(absoluteToolsDir)
	if err != nil {
		return fmt.Errorf("read demo tools dir %q: %w", absoluteToolsDir, err)
	}

	var refs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		toolDir := filepath.Join(absoluteToolsDir, entry.Name())
		if _, err := os.Stat(filepath.Join(toolDir, "tool.yaml")); err != nil {
			continue
		}
		refs = append(refs, runtime.HostPrefix+toolDir)
	}

	sort.Strings(refs)
	if len(refs) == 0 {
		return fmt.Errorf("no demo tools found in %q", absoluteToolsDir)
	}

	for _, ref := range refs {
		logger.Debug("registering demo tool", "ref", ref)
		if _, err := registry.Register(ctx, ref, ""); err != nil {
			return fmt.Errorf("register demo tool %q: %w", ref, err)
		}
	}

	return nil
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
