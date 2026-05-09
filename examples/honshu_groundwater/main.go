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
	"sort"
	"strings"
	"time"

	orchestrate "github.com/tsumina/dango/internal/engine"
	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
	storepkg "github.com/tsumina/dango/internal/store"
	runtimepkg "github.com/tsumina/dango/internal/store/runtime"
	"github.com/tsumina/dango/internal/streamrender"
	"golang.org/x/term"
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

type exampleRunResult struct {
	RequestID       string
	RunnerID        string
	ArtifactsDir    string
	PersistencePath string
	FinalRunnerView *runnerpkg.RunnerView
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

func runHonshuGroundwaterExample(ctx context.Context, cfg exampleConfig) (_ *exampleRunResult, err error) {
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
	persistencePath := filepath.Join(artifactsDir, "persistence", "dango.db")
	persistence, err := runtimepkg.Open(runtimepkg.Config{SQLitePath: persistencePath})
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := persistence.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
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
		"persistence_db", persistencePath,
		"measurements_bytes", len(cfg.MeasurementsJSON),
		"stream_events_log", streamLogPath,
	)
	orchestrator := orchestrate.NewOrchestrator(
		orchestrate.WithOrchestratorContext(ctx),
		orchestrate.WithOrchestratorLogger(logger),
		orchestrate.WithPersistence(persistence.Backend()),
	)
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
	if err := orchestrator.AddSkillDirs(llm.ConversationConfig{MaxSteps: 32}, skillDirs...); err != nil {
		return nil, err
	}
	renderCfg := streamrender.DefaultConfig()
	renderCfg.ExchangeDir = filepath.Join(artifactsDir, "exchanges")
	renderCfg.Debug = logger.Enabled(ctx, slog.LevelDebug)
	if file, ok := cfg.Out.(*os.File); ok {
		if info, err := file.Stat(); err == nil {
			renderCfg.Color = info.Mode()&os.ModeCharDevice != 0
		}
		if width, _, err := term.GetSize(int(file.Fd())); err == nil && width > 20 {
			renderCfg.MaxLineWidth = width
		}
	}

	request := buildGroundwaterRequest(cfg.MeasurementsJSON)
	logger.Info("submitting request to orchestrator")
	resp, err := orchestrator.StartRequest(ctx, orchestrate.Request{
		Input:        request,
		ArtifactsDir: artifactsDir,
	})
	if err != nil {
		return nil, fmt.Errorf("start request: %w", err)
	}
	events, err := resp.Stream.Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(8192))
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
	runnerID, err := waitForRunnerCreated(ctx, resp.Stream)
	if err != nil {
		_ = closeEventStream()
		return nil, err
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
	if err := waitForRequestRunnerSettled(ctx, resp.Stream, runnerID); err != nil {
		_ = closeEventStream()
		return nil, err
	}
	if err := waitForPersistedTerminalRunnerState(ctx, persistence.EventLogStore(), resp.RequestID, runnerID); err != nil {
		_ = closeEventStream()
		return nil, err
	}
	logger.Info("runner settled", "runner_id", runnerID, "status", view.State.Status, "phase", view.Phase)
	if err := closeEventStream(); err != nil {
		return nil, err
	}
	logger.Info("request persisted", "request_id", resp.RequestID, "runner_id", runnerID)
	describeView, err := orchestrator.DescribeRequest(ctx, resp.RequestID)
	if err != nil {
		return nil, fmt.Errorf("describe persisted request: %w", err)
	}
	runnerRecords, err := orchestrator.LoadRunnerRecords(ctx, runnerID)
	if err != nil {
		return nil, fmt.Errorf("load persisted runner records: %w", err)
	}
	cursor, err := persistence.SnapshotCursorStore().LoadCursor(ctx, resp.RequestID)
	if err != nil {
		return nil, fmt.Errorf("load describe cursor: %w", err)
	}
	logger.Info("describe replay completed",
		"request_id", resp.RequestID,
		"runner_id", describeView.RunnerID,
		"event_sequence", describeView.LatestEventSequence,
		"runner_records", len(runnerRecords),
	)
	if err := writePersistenceDebugArtifacts(artifactsDir, streamLogPath, persistencePath, describeView, runnerRecords, cursor); err != nil {
		return nil, fmt.Errorf("write persistence debug artifacts: %w", err)
	}
	logger.Info("persistence summaries written",
		"request_id", resp.RequestID,
		"debug_dir", filepath.Join(artifactsDir, "debug"),
	)
	result := &exampleRunResult{
		RequestID:       resp.RequestID,
		RunnerID:        runnerID,
		ArtifactsDir:    artifactsDir,
		PersistencePath: persistencePath,
		FinalRunnerView: view,
	}
	if view.State.Status == runnerpkg.RunnerStatusFailed {
		errText := strings.TrimSpace(view.State.Error)
		if errText == "" {
			errText = "no error context recorded"
		}
		return result, fmt.Errorf("runner settled with failure: %s", errText)
	}
	return result, nil
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

type describeViewSummary struct {
	RequestID                string                         `json:"request_id"`
	RunnerID                 string                         `json:"runner_id,omitempty"`
	Phase                    runnerpkg.RunnerPhase          `json:"phase,omitempty"`
	Status                   runnerpkg.RunnerStatus         `json:"status,omitempty"`
	NodeCount                int                            `json:"node_count"`
	Nodes                    []describeNodeSummary          `json:"nodes,omitempty"`
	ArtifactCount            int                            `json:"artifact_count"`
	Artifacts                []orchestrate.DescribeArtifact `json:"artifacts,omitempty"`
	LatestCheckpointSequence int64                          `json:"latest_checkpoint_sequence,omitempty"`
	LatestEventSequence      uint64                         `json:"latest_event_sequence,omitempty"`
}

type describeNodeSummary struct {
	ID        string   `json:"id"`
	SkillName string   `json:"skill_name,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
	Status    string   `json:"status,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type runnerRecordsSummary struct {
	RecordCount int                        `json:"record_count"`
	Records     []runnerRecordSummaryEntry `json:"records,omitempty"`
}

type runnerRecordSummaryEntry struct {
	Seq          int64                      `json:"seq"`
	Kind         runnerpkg.RunnerRecordKind `json:"kind"`
	Timestamp    time.Time                  `json:"ts"`
	Status       runnerpkg.RunnerStatus     `json:"status,omitempty"`
	Error        string                     `json:"error,omitempty"`
	EventType    string                     `json:"event_type,omitempty"`
	NodeID       string                     `json:"node_id,omitempty"`
	DataEncoding string                     `json:"data_encoding,omitempty"`
}

type persistenceSummary struct {
	RequestID                string                 `json:"request_id"`
	RunnerID                 string                 `json:"runner_id,omitempty"`
	Phase                    runnerpkg.RunnerPhase  `json:"phase,omitempty"`
	Status                   runnerpkg.RunnerStatus `json:"status,omitempty"`
	PersistencePath          string                 `json:"persistence_path"`
	StreamEventsPath         string                 `json:"stream_events_path"`
	DescribeViewPath         string                 `json:"describe_view_path"`
	RunnerRecordsPath        string                 `json:"runner_records_path"`
	DescribeNodeCount        int                    `json:"describe_node_count"`
	DescribeArtifactCount    int                    `json:"describe_artifact_count"`
	RunnerRecordCount        int                    `json:"runner_record_count"`
	LatestCheckpointSequence int64                  `json:"latest_checkpoint_sequence,omitempty"`
	LatestEventSequence      uint64                 `json:"latest_event_sequence,omitempty"`
	Cursor                   snapshotCursorSummary  `json:"cursor"`
}

type snapshotCursorSummary struct {
	RequestID          string    `json:"request_id"`
	RunnerID           string    `json:"runner_id,omitempty"`
	CheckpointSequence int64     `json:"checkpoint_sequence,omitempty"`
	EventSequence      uint64    `json:"event_sequence,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}

func writePersistenceDebugArtifacts(artifactsDir string, streamLogPath string, persistencePath string, describeView *orchestrate.DescribeView, runnerRecords []runnerpkg.RunnerRecord, cursor storepkg.SnapshotCursor) error {
	debugDir := filepath.Join(artifactsDir, "debug")
	describePath := filepath.Join(debugDir, "describe_view.json")
	runnerRecordsPath := filepath.Join(debugDir, "runner_records.json")
	persistenceSummaryPath := filepath.Join(debugDir, "persistence_summary.json")

	describeSummary := summarizeDescribeView(describeView)
	if err := writeJSONArtifact(describePath, describeSummary); err != nil {
		return err
	}
	runnerSummary := summarizeRunnerRecords(runnerRecords)
	if err := writeJSONArtifact(runnerRecordsPath, runnerSummary); err != nil {
		return err
	}
	if err := writeJSONArtifact(persistenceSummaryPath, persistenceSummary{
		RequestID:                describeSummary.RequestID,
		RunnerID:                 describeSummary.RunnerID,
		Phase:                    describeSummary.Phase,
		Status:                   describeSummary.Status,
		PersistencePath:          persistencePath,
		StreamEventsPath:         streamLogPath,
		DescribeViewPath:         describePath,
		RunnerRecordsPath:        runnerRecordsPath,
		DescribeNodeCount:        describeSummary.NodeCount,
		DescribeArtifactCount:    describeSummary.ArtifactCount,
		RunnerRecordCount:        runnerSummary.RecordCount,
		LatestCheckpointSequence: describeSummary.LatestCheckpointSequence,
		LatestEventSequence:      describeSummary.LatestEventSequence,
		Cursor:                   summarizeSnapshotCursor(cursor),
	}); err != nil {
		return err
	}
	return nil
}

func summarizeDescribeView(view *orchestrate.DescribeView) describeViewSummary {
	if view == nil {
		return describeViewSummary{}
	}
	nodeIDs := make([]string, 0, len(view.Nodes))
	for nodeID := range view.Nodes {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)
	nodes := make([]describeNodeSummary, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		node := view.Nodes[nodeID]
		nodes = append(nodes, describeNodeSummary{
			ID:        node.ID,
			SkillName: node.SkillName,
			DependsOn: append([]string(nil), node.DependsOn...),
			Status:    node.Status,
			Error:     node.Error,
		})
	}
	artifacts := append([]orchestrate.DescribeArtifact(nil), view.Artifacts...)
	return describeViewSummary{
		RequestID:                view.RequestID,
		RunnerID:                 view.RunnerID,
		Phase:                    view.Phase,
		Status:                   view.Status,
		NodeCount:                len(nodes),
		Nodes:                    nodes,
		ArtifactCount:            len(artifacts),
		Artifacts:                artifacts,
		LatestCheckpointSequence: view.LatestCheckpointSequence,
		LatestEventSequence:      view.LatestEventSequence,
	}
}

func summarizeRunnerRecords(records []runnerpkg.RunnerRecord) runnerRecordsSummary {
	entries := make([]runnerRecordSummaryEntry, 0, len(records))
	for _, record := range records {
		entry := runnerRecordSummaryEntry{
			Seq:       record.Seq,
			Kind:      record.Kind,
			Timestamp: record.Timestamp,
			Status:    record.Status,
			Error:     record.Error,
		}
		if record.Event != nil {
			entry.EventType = record.Event.Type
			entry.NodeID = record.Event.NodeID
			entry.DataEncoding = record.Event.DataEncoding
		}
		entries = append(entries, entry)
	}
	return runnerRecordsSummary{RecordCount: len(entries), Records: entries}
}

func summarizeSnapshotCursor(cursor storepkg.SnapshotCursor) snapshotCursorSummary {
	return snapshotCursorSummary{
		RequestID:          cursor.RequestID,
		RunnerID:           cursor.RunnerID,
		CheckpointSequence: cursor.CheckpointSequence,
		EventSequence:      cursor.EventSequence,
		UpdatedAt:          cursor.UpdatedAt,
	}
}

func writeJSONArtifact(path string, value any) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", path, closeErr)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	return nil
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
	return renderer.RenderSubscriptionObserved(ctx, sub, func(event streampkg.Event) error {
		if encoder != nil {
			if err := encoder.Encode(event); err != nil {
				return err
			}
		}
		return nil
	})
}

func waitForRunnerCreated(ctx context.Context, eventStream *streampkg.Stream) (string, error) {
	if eventStream == nil {
		return "", fmt.Errorf("request stream is nil")
	}
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventStatusProgress, streampkg.EventStatusFailed}}, streampkg.WithSubscriberBuffer(64))
	if err != nil {
		return "", err
	}
	defer sub.Cancel()
	for {
		event, ok, err := sub.Next(ctx)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("request stream closed before runner creation")
		}
		if event.From.Layer != "orchestrator" {
			continue
		}
		values := map[string]any{}
		_ = json.Unmarshal(event.Delta, &values)
		if event.EventType == streampkg.EventStatusFailed {
			if msg, ok := values["message"].(string); ok && msg != "" {
				return "", fmt.Errorf("request rejected: %s", msg)
			}
			var text string
			_ = json.Unmarshal(event.Delta, &text)
			if text == "" {
				text = "request rejected"
			}
			return "", fmt.Errorf("request rejected: %s", text)
		}
		if msg, _ := values["message"].(string); msg != "runner created" {
			continue
		}
		if runnerID, _ := values["runner_id"].(string); runnerID != "" {
			return runnerID, nil
		}
	}
}

func waitForRequestRunnerSettled(ctx context.Context, eventStream *streampkg.Stream, runnerID string) error {
	if eventStream == nil {
		return fmt.Errorf("request stream is nil")
	}
	sub, err := eventStream.Subscribe(streampkg.Filter{}, streampkg.WithReplayLast(64), streampkg.WithSubscriberBuffer(64))
	if err != nil {
		return err
	}
	defer sub.Cancel()
	for {
		event, ok, err := sub.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("request stream closed before runner settled update")
		}
		if event.EventType != streampkg.EventRunnerPhaseChanged || event.Scope.RunnerID != runnerID {
			continue
		}
		values := map[string]any{}
		_ = json.Unmarshal(event.Delta, &values)
		if phase, _ := values["phase"].(string); phase == string(runnerpkg.PhaseSettled) {
			return nil
		}
	}
}

func waitForPersistedTerminalRunnerState(ctx context.Context, eventLog storepkg.EventLogStore, requestID string, runnerID string) error {
	if eventLog == nil {
		return fmt.Errorf("runtime persistence event log store is nil")
	}
	if strings.TrimSpace(requestID) == "" {
		return fmt.Errorf("request id must not be empty")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	nextSequence := uint64(1)
	for {
		events, err := eventLog.LoadEvents(ctx, streampkg.Scope{RequestID: requestID}, nextSequence, streampkg.Filter{})
		if err != nil {
			return err
		}
		if len(events) > 0 {
			nextSequence = events[len(events)-1].SequenceNumber + 1
		}
		if hasPersistedTerminalRunnerState(events, runnerID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func hasPersistedTerminalRunnerState(events []streampkg.Event, runnerID string) bool {
	for _, event := range events {
		expanded, err := streampkg.ExpandBundleEvent(event)
		if err != nil {
			continue
		}
		for _, candidate := range expanded {
			if candidate.EventType != streampkg.EventRunnerPhaseChanged {
				continue
			}
			if runnerID != "" && candidate.Scope.RunnerID != "" && candidate.Scope.RunnerID != runnerID {
				continue
			}
			var delta struct {
				Phase  string `json:"phase"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(candidate.Delta, &delta); err != nil {
				continue
			}
			if runnerpkg.RunnerPhase(delta.Phase) != runnerpkg.PhaseSettled {
				continue
			}
			switch runnerpkg.RunnerStatus(delta.Status) {
			case runnerpkg.RunnerStatusIdle, runnerpkg.RunnerStatusFailed, runnerpkg.RunnerStatusCanceled:
				return true
			}
		}
	}
	return false
}

func exampleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate example root")
	}
	return filepath.Dir(file), nil
}
