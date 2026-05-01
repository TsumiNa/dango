package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	orchestrate "github.com/tsumina/dango/internal/engine"
	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	"github.com/tsumina/dango/internal/llm"
)

//go:embed sample_measurements.json
var embeddedSampleMeasurements string

const defaultExampleTimeout = 5 * time.Minute

type exampleConfig struct {
	MeasurementsJSON string
	ArtifactsDir     string
	Out              io.Writer
	Logger           *slog.Logger
	LLMClient        *llm.Client
	EnvFiles         []string
}

func main() {
	var inputPath string
	var timeout time.Duration
	var logLevel string
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	flags.StringVar(&inputPath, "input", "", "path to messy groundwater JSON; uses embedded sample when empty")
	flags.DurationVar(&timeout, "timeout", defaultExampleTimeout, "overall run timeout; set 0 to disable")
	flags.StringVar(&logLevel, "log-level", "info", "stderr log level: debug, info, warn, or error")
	if err := flags.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	logger, err := newExampleLogger(os.Stderr, logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	measurements := embeddedSampleMeasurements
	if inputPath != "" {
		data, err := os.ReadFile(inputPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read input: %v\n", err)
			os.Exit(1)
		}
		measurements = string(data)
	}

	ctx := context.Background()
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	if _, err := runHonshuGroundwaterExample(ctx, exampleConfig{
		MeasurementsJSON: measurements,
		Out:              os.Stdout,
		Logger:           logger,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "run example: %v\n", err)
		os.Exit(1)
	}
}

func runHonshuGroundwaterExample(ctx context.Context, cfg exampleConfig) (*runnerpkg.RunnerView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(cfg.MeasurementsJSON) == "" {
		return nil, fmt.Errorf("measurements JSON must not be empty")
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	artifactsDir := cfg.ArtifactsDir
	if artifactsDir == "" {
		root, err := exampleRoot()
		if err != nil {
			return nil, err
		}
		artifactsDir = filepath.Join(root, "artifacts")
	}
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return nil, err
	}
	orLog, orLogPath, err := createArtifactLog(artifactsDir, "orchestrator_stream.ndjson")
	if err != nil {
		return nil, err
	}
	defer orLog.Close()
	runnerLog, runnerLogPath, err := createArtifactLog(artifactsDir, "runner_updates.ndjson")
	if err != nil {
		return nil, err
	}
	defer runnerLog.Close()

	root, err := exampleRoot()
	if err != nil {
		return nil, err
	}
	logger.Info("honshu groundwater example starting",
		"example_root", root,
		"artifacts_dir", artifactsDir,
		"measurements_bytes", len(cfg.MeasurementsJSON),
		"orchestrator_stream_log", orLogPath,
		"runner_updates_log", runnerLogPath,
	)
	logger.Info("loading llm client")
	client, err := resolveExampleLLMClient(cfg)
	if err != nil {
		return nil, err
	}
	logger.Info("llm client ready",
		"provider", client.Provider(),
		"model", client.Model(),
		"reasoning_effort", client.ReasoningEffort(),
	)
	orchestrator, err := configureExampleOrchestrator(ctx, root, client, logger)
	if err != nil {
		return nil, err
	}

	request := buildGroundwaterRequest(cfg.MeasurementsJSON)
	logger.Info("submitting request to orchestrator")
	runnerID, err := orchestrator.StartRequestWithProgress(ctx, &orchestrate.Request{
		Input:        request,
		ArtifactsDir: artifactsDir,
	}, exampleOrchestratorProgress(cfg.Out, logger, orLog))
	if err != nil {
		logger.Error("request failed before runner stream was available", "err", err)
		return nil, fmt.Errorf("start request before runner stream is available: %w", err)
	}
	logger.Info("runner created", "runner_id", runnerID)
	fmt.Fprintf(cfg.Out, "or: runner created runner_id=%s\n", runnerID)
	updates, unsubscribe, err := orchestrator.SubscribeRunner(runnerID, 64)
	if err != nil {
		return nil, err
	}
	defer unsubscribe()
	logger.Info("streaming runner updates", "runner_id", runnerID)
	if err := streamRunnerUpdates(ctx, cfg.Out, runnerLog, updates); err != nil {
		logger.Error("runner update stream failed", "runner_id", runnerID, "err", err)
		return nil, err
	}

	logger.Info("waiting for runner to settle", "runner_id", runnerID)
	view, err := orchestrator.WaitRunner(ctx, runnerID)
	if err != nil {
		logger.Error("runner wait failed", "runner_id", runnerID, "err", err)
		return nil, err
	}
	if view == nil || view.Phase != runnerpkg.PhaseSettled {
		return nil, fmt.Errorf("runner did not settle: %+v", view)
	}
	logger.Info("runner settled", "runner_id", runnerID, "status", view.State.Status, "phase", view.Phase)
	return view, nil
}

func resolveExampleLLMClient(cfg exampleConfig) (*llm.Client, error) {
	if cfg.LLMClient != nil {
		return cfg.LLMClient, nil
	}
	client, err := llm.NewClientFromEnv(cfg.EnvFiles...)
	if err != nil {
		return nil, fmt.Errorf("load LLM client from .env: %w", err)
	}
	return client, nil
}

func configureExampleOrchestrator(ctx context.Context, root string, client *llm.Client, logger *slog.Logger) (*orchestrate.Orchestrator, error) {
	if client == nil {
		return nil, fmt.Errorf("example requires a non-nil LLM client")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	o := orchestrate.NewOrchestrator(ctx, logger)

	logger.Info("configuring orchestrator skill")
	plannerSkill, err := orchestrate.NewEmbeddedOrchestratorSkill(client, nil, nil)
	if err != nil {
		return nil, err
	}
	if err := o.SetOrchestratorSkill(plannerSkill); err != nil {
		return nil, err
	}

	skillCfg := &llm.ConversationConfig{MaxSteps: 12}
	skillDirs := []string{
		filepath.Join(root, "elevation_lookup"),
		filepath.Join(root, "train_gp_model"),
		filepath.Join(root, "markdown_to_pdf"),
	}
	for _, dir := range skillDirs {
		logger.Info("registering skill", "skill_dir", dir)
	}
	if err := o.AddSkillFromDirs(client, skillCfg, skillDirs...); err != nil {
		return nil, err
	}

	return o, nil
}

func newExampleLogger(out io.Writer, level string) (*slog.Logger, error) {
	if out == nil {
		out = io.Discard
	}
	var slogLevel slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		slogLevel = slog.LevelInfo
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		return nil, fmt.Errorf("invalid log level %q; use debug, info, warn, or error", level)
	}
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slogLevel})), nil
}

func createArtifactLog(artifactsDir string, name string) (*os.File, string, error) {
	path := filepath.Join(artifactsDir, name)
	file, err := os.Create(path)
	if err != nil {
		return nil, "", err
	}
	return file, path, nil
}

func exampleOrchestratorProgress(out io.Writer, logger *slog.Logger, raw io.Writer) orchestrate.OrchestratorProgressFunc {
	encoder := json.NewEncoder(raw)
	var textBytes int
	return func(event orchestrate.OrchestratorProgressEvent) {
		if raw != nil {
			if err := encoder.Encode(event); err != nil && logger != nil {
				logger.Error("write orchestrator progress artifact failed", "err", err)
			}
		}
		switch event.Type {
		case orchestrate.OrchestratorProgressStatus:
			if logger != nil {
				logger.Info("orchestrator progress", "message", event.Message)
			}
			fmt.Fprintf(out, "or: %s\n", event.Message)
		case orchestrate.OrchestratorProgressReasoning:
			if line := compactTerminalText(event.Delta); line != "" {
				fmt.Fprintf(out, "or reasoning: %s\n", line)
			}
		case orchestrate.OrchestratorProgressText:
			textBytes += len(event.Delta)
			if logger != nil {
				logger.Debug("orchestrator plan text received", "bytes", textBytes)
			}
		}
	}
}

func buildGroundwaterRequest(measurements string) string {
	return "Use the following messy JSON to build a model that predicts groundwater water level at arbitrary Honshu locations. Save prediction values as CSV for later analysis. Do not make a PDF.\n\n```json\n" +
		strings.TrimSpace(measurements) +
		"\n```"
}

func streamRunnerUpdates(ctx context.Context, out io.Writer, raw io.Writer, updates <-chan runnerpkg.RunnerUpdate) error {
	encoder := json.NewEncoder(raw)
	var lastLine string
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			if raw != nil {
				if err := encoder.Encode(update); err != nil {
					return err
				}
			}
			line := compactRunnerUpdate(update)
			if line != "" && line != lastLine {
				if _, err := fmt.Fprintln(out, line); err != nil {
					return err
				}
				lastLine = line
			}
		}
	}
}

func compactRunnerUpdate(update runnerpkg.RunnerUpdate) string {
	event := ""
	node := ""
	if update.Event != nil {
		event = update.Event.Type.String()
		node = update.Event.NodeID
	}
	pending := 0
	for _, count := range update.Snapshot.PendingNodes {
		pending += count
	}
	completed := len(update.Snapshot.CompletedNodes)
	base := fmt.Sprintf("ru: runner_id=%s status=%s phase=%s active=%d completed=%d pending=%d",
		update.RunnerID,
		update.State.Status,
		update.Phase,
		update.Snapshot.ActiveCount,
		completed,
		pending,
	)
	if update.State.Error != "" {
		base += fmt.Sprintf(" error=%q", compactTerminalText(update.State.Error))
	}
	if event == "" {
		return base
	}
	if update.Event != nil && update.Event.Type == runnerpkg.EventNodeFailed && update.Event.Data != nil {
		base += fmt.Sprintf(" detail=%q", compactTerminalText(fmt.Sprint(update.Event.Data)))
	}
	if node != "" {
		return base + " event=" + event + " node=" + node
	}
	return base + " event=" + event
}

func compactTerminalText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 240 {
		text = text[:240] + "..."
	}
	return text
}

func exampleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate example root")
	}
	return filepath.Dir(file), nil
}
