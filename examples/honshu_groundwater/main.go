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
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/streamrender"
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
	streamLog, streamLogPath, err := createArtifactLog(filepath.Join(artifactsDir, "debug"), "stream_events.jsonl")
	if err != nil {
		return nil, err
	}
	defer streamLog.Close()
	root, err := exampleRoot()
	if err != nil {
		return nil, err
	}
	logger.Info("honshu groundwater example starting",
		"example_root", root,
		"artifacts_dir", artifactsDir,
		"measurements_bytes", len(cfg.MeasurementsJSON),
		"stream_events_log", streamLogPath,
	)
	orchestrator := orchestrate.NewOrchestrator(ctx, logger)
	if cfg.LLMClient != nil {
		logger.Info("using configured llm client",
			"provider", cfg.LLMClient.Provider(),
			"model", cfg.LLMClient.Model(),
			"reasoning_effort", cfg.LLMClient.ReasoningEffort(),
		)
		if err := orchestrator.SetClient(cfg.LLMClient); err != nil {
			return nil, err
		}
	}
	skillDirs := []string{
		filepath.Join(root, "elevation_lookup"),
		filepath.Join(root, "train_gp_model"),
		filepath.Join(root, "markdown_to_pdf"),
	}
	for _, dir := range skillDirs {
		logger.Info("registering skill", "skill_dir", dir)
	}
	if err := orchestrator.AddSkillDirs(&llm.ConversationConfig{MaxSteps: 12}, skillDirs...); err != nil {
		return nil, err
	}
	renderCfg := streamrender.DefaultConfig()
	renderCfg.ExchangeDir = filepath.Join(artifactsDir, "exchanges")
	renderCfg.HiddenEventTypes = []string{
		streampkg.EventLLMReasoningDelta,
		streampkg.EventLLMToolCallStarted,
		streampkg.EventLLMToolCallDelta,
		streampkg.EventLLMToolCallCompleted,
		streampkg.EventLLMToolResultDelta,
		streampkg.EventToolExecutionStarted,
		streampkg.EventToolExecutionCompleted,
		streampkg.EventToolExecutionFailed,
	}
	if file, ok := cfg.Out.(*os.File); ok {
		if info, err := file.Stat(); err == nil {
			renderCfg.Color = info.Mode()&os.ModeCharDevice != 0
		}
	}

	request := buildGroundwaterRequest(cfg.MeasurementsJSON)
	logger.Info("submitting request to orchestrator")
	resp, err := orchestrator.StartRequest(ctx, orchestrate.Request{
		Input:        request,
		ArtifactsDir: artifactsDir,
	})
	if err != nil {
		logger.Error("request failed before runner stream was available", "err", err)
		return nil, fmt.Errorf("start request before runner stream is available: %w", err)
	}
	runnerID := resp.RunnerID
	events, err := resp.Stream.Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(256))
	if err != nil {
		return nil, err
	}
	eventErrCh := make(chan error, 1)
	go func() {
		eventErrCh <- streamExampleEvents(ctx, cfg.Out, streamLog, events, renderCfg)
	}()
	closeEventStream := func() error {
		resp.Stream.Close()
		return <-eventErrCh
	}
	logger.Info("runner created", "runner_id", runnerID)

	logger.Info("waiting for runner to settle", "runner_id", runnerID)
	view, err := orchestrator.WaitRunner(ctx, runnerID)
	if err != nil {
		logger.Error("runner wait failed", "runner_id", runnerID, "err", err)
		_ = closeEventStream()
		return nil, err
	}
	if view == nil || view.Phase != runnerpkg.PhaseSettled {
		_ = closeEventStream()
		return nil, fmt.Errorf("runner did not settle: %+v", view)
	}
	logger.Info("runner settled", "runner_id", runnerID, "status", view.State.Status, "phase", view.Phase)
	if err := closeEventStream(); err != nil {
		return nil, err
	}
	return view, nil
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
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return nil, "", err
	}
	path := filepath.Join(artifactsDir, name)
	file, err := os.Create(path)
	if err != nil {
		return nil, "", err
	}
	return file, path, nil
}

func buildGroundwaterRequest(measurements string) string {
	return "Use the following messy JSON to build a model that predicts groundwater water level at arbitrary Honshu locations. Save prediction values as CSV for later analysis. Do not make a PDF.\n\n```json\n" +
		strings.TrimSpace(measurements) +
		"\n```"
}

func streamExampleEvents(ctx context.Context, out io.Writer, raw io.Writer, sub *streampkg.Subscription, renderCfg streamrender.Config) error {
	var encoder *json.Encoder
	if raw != nil {
		encoder = json.NewEncoder(raw)
	}
	renderer := streamrender.New(out, renderCfg)
	for {
		event, ok, err := sub.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if encoder != nil {
			if err := encoder.Encode(event); err != nil {
				return err
			}
		}
		if err := renderer.RenderEvent(event); err != nil {
			return err
		}
	}
}

func exampleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate example root")
	}
	return filepath.Dir(file), nil
}
