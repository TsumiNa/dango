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
	streamLog, streamLogPath, err := createArtifactLog(artifactsDir, "stream_events.jsonl")
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
		eventErrCh <- streamExampleEvents(ctx, cfg.Out, streamLog, events)
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

func buildGroundwaterRequest(measurements string) string {
	return "Use the following messy JSON to build a model that predicts groundwater water level at arbitrary Honshu locations. Save prediction values as CSV for later analysis. Do not make a PDF.\n\n```json\n" +
		strings.TrimSpace(measurements) +
		"\n```"
}

func streamExampleEvents(ctx context.Context, out io.Writer, raw io.Writer, sub *streampkg.Subscription) error {
	encoder := json.NewEncoder(raw)
	var lastLine string
	for {
		event, ok, err := sub.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if raw != nil {
			if err := encoder.Encode(event); err != nil {
				return err
			}
		}
		line := compactStreamEvent(event)
		if line != "" && line != lastLine {
			if _, err := fmt.Fprintln(out, line); err != nil {
				return err
			}
			lastLine = line
		}
	}
}

func compactStreamEvent(event streampkg.Event) string {
	switch event.EventType {
	case streampkg.EventLLMReasoningDelta:
		if event.From.Layer == "orchestrator" {
			if line := compactTerminalText(deltaString(event)); line != "" {
				return "or reasoning: " + line
			}
		}
	case streampkg.EventStatusProgress:
		if event.From.Layer == "orchestrator" {
			if msg, runnerID := deltaMessageAndRunner(deltaMap(event)); msg != "" {
				if runnerID != "" {
					return fmt.Sprintf("or: %s runner_id=%s", msg, runnerID)
				}
				return "or: " + msg
			}
		}
	case streampkg.EventStatusStarted:
		if event.From.Layer == "orchestrator" {
			if line := compactTerminalText(deltaString(event)); line != "" {
				return "or: " + line
			}
		}
	case streampkg.EventRunnerPhaseChanged:
		values := deltaMap(event)
		phase := stringValue(values["phase"])
		status := stringValue(values["status"])
		if phase == "" {
			phase = "unknown"
		}
		return fmt.Sprintf("ru: runner_id=%s status=%s phase=%s", event.Scope.RunnerID, status, phase)
	case streampkg.EventRunnerNodeStarted, streampkg.EventRunnerNodeCompleted, streampkg.EventRunnerNodeFailed:
		values := deltaMap(event)
		nodeID := stringValue(values["node_id"])
		if nodeID == "" {
			nodeID = event.Scope.NodeID
		}
		eventName := strings.TrimPrefix(event.EventType, "runner.")
		line := fmt.Sprintf("ru: runner_id=%s status=%s event=%s node=%s", event.Scope.RunnerID, event.Status, eventName, nodeID)
		if skill := stringValue(event.Metadata["skill_name"]); skill != "" {
			line += " skill=" + skill
		}
		if errText := stringValue(values["error"]); errText != "" {
			line += fmt.Sprintf(" error=%q", compactTerminalText(errText))
		}
		return line
	case streampkg.EventExecutorPolishStarted, streampkg.EventExecutorPolishCompleted, streampkg.EventExecutorPolishFailed,
		streampkg.EventExecutorExecuteStarted, streampkg.EventExecutorExecuteCompleted, streampkg.EventExecutorExecuteFailed,
		streampkg.EventExecutorReportStarted, streampkg.EventExecutorReportCompleted, streampkg.EventExecutorReportFailed:
		values := deltaMap(event)
		nodeID := stringValue(values["node_id"])
		if nodeID == "" {
			nodeID = event.Scope.NodeID
		}
		eventName := strings.TrimPrefix(event.EventType, "executor.")
		line := fmt.Sprintf("ex: runner_id=%s status=%s event=%s node=%s", event.Scope.RunnerID, event.Status, eventName, nodeID)
		if skill := stringValue(event.Metadata["skill_name"]); skill != "" {
			line += " skill=" + skill
		}
		if errText := stringValue(values["error"]); errText != "" {
			line += fmt.Sprintf(" error=%q", compactTerminalText(errText))
		}
		return line
	case streampkg.EventLLMToolCallCompleted:
		values := deltaMap(event)
		line := fmt.Sprintf("sk: skill=%s model requested tool=%s call=%s", skillName(event), stringValue(values["name"]), stringValue(values["call_id"]))
		if args := stringValue(values["arguments"]); args != "" {
			line += fmt.Sprintf(" args=%q", compactTerminalText(args))
		}
		return line
	case streampkg.EventToolExecutionStarted, streampkg.EventToolExecutionCompleted, streampkg.EventToolExecutionFailed:
		values := deltaMap(event)
		eventName := strings.TrimPrefix(event.EventType, "tool.")
		line := fmt.Sprintf("sk: skill=%s status=%s event=%s tool=%s call=%s",
			skillName(event), event.Status, eventName, stringValue(values["name"]), stringValue(values["call_id"]))
		if errText := stringValue(values["error"]); errText != "" {
			line += fmt.Sprintf(" error=%q", compactTerminalText(errText))
		}
		return line
	case streampkg.EventLLMToolResultDelta:
		values := deltaMap(event)
		line := fmt.Sprintf("sk: skill=%s recorded tool result status=%s tool=%s call=%s",
			skillName(event), event.Status, stringValue(values["name"]), stringValue(values["call_id"]))
		if errText := stringValue(values["error"]); errText != "" {
			line += fmt.Sprintf(" error=%q", compactTerminalText(errText))
		}
		return line
	case streampkg.EventStatusFailed:
		if line := compactTerminalText(deltaString(event)); line != "" {
			return fmt.Sprintf("%s: failed %q", event.From.Layer, line)
		}
	}
	return ""
}

func deltaString(event streampkg.Event) string {
	var text string
	if err := json.Unmarshal(event.Delta, &text); err == nil {
		return text
	}
	return ""
}

func deltaMap(event streampkg.Event) map[string]any {
	var values map[string]any
	if err := json.Unmarshal(event.Delta, &values); err != nil {
		return nil
	}
	return values
}

func deltaMessageAndRunner(values map[string]any) (string, string) {
	if len(values) == 0 {
		return "", ""
	}
	return stringValue(values["message"]), stringValue(values["runner_id"])
}

func skillName(event streampkg.Event) string {
	if skill := stringValue(event.Metadata["skill_name"]); skill != "" {
		return skill
	}
	if event.From.ID != "" {
		return event.From.ID
	}
	return "unknown"
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
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
