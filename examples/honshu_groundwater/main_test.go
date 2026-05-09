package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	orchestrate "github.com/tsumina/dango/internal/engine"
	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
	storepkg "github.com/tsumina/dango/internal/store"
	runtimepkg "github.com/tsumina/dango/internal/store/runtime"
	"github.com/tsumina/dango/internal/streamrender"
)

func TestElevationSkillScriptParsesMessySample(t *testing.T) {
	root, err := exampleRoot()
	if err != nil {
		t.Fatalf("exampleRoot: %v", err)
	}
	input, err := json.Marshal(map[string]string{"observations_json": embeddedSampleMeasurements})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	output, err := runSkillScript(context.Background(), filepath.Join(root, "elevation_lookup"), "scripts/enrich.py", string(input))
	if err != nil {
		t.Fatalf("run elevation script: %v", err)
	}
	var payload struct {
		ObservationN int `json:"observation_n"`
		Observations []struct {
			SiteID         string  `json:"site_id"`
			Latitude       float64 `json:"latitude"`
			Longitude      float64 `json:"longitude"`
			WaterLevelMBGL float64 `json:"water_level_m_bgl"`
		} `json:"observations"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("parse script output: %v\n%s", err, output)
	}
	observations := payload.Observations
	if len(observations) != 47 {
		t.Fatalf("observation count = %d, want 47", len(observations))
	}

	var foundTokyo bool
	for _, obs := range observations {
		if obs.Latitude == 0 || obs.Longitude == 0 {
			t.Fatalf("observation has empty coordinates: %+v", obs)
		}
		if obs.WaterLevelMBGL <= 0 {
			t.Fatalf("observation has invalid water level: %+v", obs)
		}
		if obs.SiteID == "tokyo-west-upland" {
			foundTokyo = true
			if obs.WaterLevelMBGL != 3.1 {
				t.Fatalf("tokyo water level = %v, want 3.1", obs.WaterLevelMBGL)
			}
		}
	}
	if !foundTokyo {
		t.Fatal("missing tokyo-west-upland observation")
	}
}

func TestSkillScriptCommandRunsThroughStandardBashTool(t *testing.T) {
	root, err := exampleRoot()
	if err != nil {
		t.Fatalf("exampleRoot: %v", err)
	}
	sk, err := llm.NewSkill(filepath.Join(root, "elevation_lookup"), llm.DefaultSkillConfig())
	if err != nil {
		t.Fatalf("NewSkill: %v", err)
	}
	tools, err := sk.BuiltinTools()
	if err != nil {
		t.Fatalf("BuiltinTools: %v", err)
	}
	bash := findTool(t, tools, "bash")
	command, err := skillScriptCommand(filepath.Join(root, "elevation_lookup"), "scripts/enrich.py", map[string]string{
		"observations_json": embeddedSampleMeasurements,
	})
	if err != nil {
		t.Fatalf("skillScriptCommand: %v", err)
	}
	args, err := json.Marshal(map[string]any{
		"command":         command,
		"timeout_seconds": 60,
	})
	if err != nil {
		t.Fatalf("marshal bash args: %v", err)
	}
	output, err := bash.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("bash Execute: %v\n%s", err, output)
	}
	var payload struct {
		ObservationN int `json:"observation_n"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("parse bash script output: %v\n%s", err, output)
	}
	if payload.ObservationN != 47 {
		t.Fatalf("observation count = %d, want 47", payload.ObservationN)
	}
}

func TestMarkdownPDFSkillScriptRendersPDF(t *testing.T) {
	root, err := exampleRoot()
	if err != nil {
		t.Fatalf("exampleRoot: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "report.pdf")
	input, err := json.Marshal(map[string]string{
		"markdown":    "# Report\n\n- groundwater summary\n- prediction artifacts",
		"output_path": outputPath,
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	output, err := runSkillScript(context.Background(), filepath.Join(root, "markdown_to_pdf"), "scripts/render.py", string(input))
	if err != nil {
		t.Fatalf("run markdown script: %v", err)
	}
	var payload struct {
		PDFPath string `json:"pdf_path"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		t.Fatalf("parse script output: %v\n%s", err, output)
	}
	if payload.PDFPath != outputPath {
		t.Fatalf("pdf_path = %q, want %q", payload.PDFPath, outputPath)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read PDF: %v", err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Fatalf("PDF missing magic header: %q", string(data[:min(len(data), 16)]))
	}
}

func TestTrainGPSkillScriptBuildsPredictionArtifacts(t *testing.T) {
	root, err := exampleRoot()
	if err != nil {
		t.Fatalf("exampleRoot: %v", err)
	}
	elevationInput, err := json.Marshal(map[string]string{"observations_json": embeddedSampleMeasurements})
	if err != nil {
		t.Fatalf("marshal elevation input: %v", err)
	}
	enriched, err := runSkillScript(context.Background(), filepath.Join(root, "elevation_lookup"), "scripts/enrich.py", string(elevationInput))
	if err != nil {
		t.Fatalf("run elevation script: %v", err)
	}

	artifactsDir := t.TempDir()
	trainInput, err := json.Marshal(map[string]string{
		"parent_exchange": "```json\n" + enriched + "\n```",
		"artifacts_dir":   artifactsDir,
	})
	if err != nil {
		t.Fatalf("marshal train input: %v", err)
	}
	output, err := runSkillScript(context.Background(), filepath.Join(root, "train_gp_model"), "scripts/train.py", string(trainInput))
	if err != nil {
		t.Fatalf("run train script: %v", err)
	}
	var result trainingResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("parse train output: %v\n%s", err, output)
	}
	if result.PredictionCount != 36 {
		t.Fatalf("prediction count = %d, want 36", result.PredictionCount)
	}
	if _, err := os.Stat(result.CSVPath); err != nil {
		t.Fatalf("stat CSV: %v", err)
	}
	if _, err := os.Stat(result.PlotPath); err != nil {
		t.Fatalf("stat plot: %v", err)
	}
	if !strings.HasPrefix(result.CSVPath, artifactsDir) {
		t.Fatalf("csv path = %q, want under %q", result.CSVPath, artifactsDir)
	}
}

func TestRunHonshuGroundwaterExampleExecutesNeededSkills(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := newFakeLLMClient(t)
	artifactsDir := t.TempDir()
	var stream bytes.Buffer
	var logs bytes.Buffer
	logger, err := newExampleLogger(&logs, "debug")
	if err != nil {
		t.Fatalf("newExampleLogger: %v", err)
	}

	result, err := runHonshuGroundwaterExample(ctx, exampleConfig{
		MeasurementsJSON: embeddedSampleMeasurements,
		ArtifactsDir:     artifactsDir,
		Out:              &stream,
		Logger:           logger,
		LLMClient:        client,
	})
	if err != nil {
		t.Fatalf("runHonshuGroundwaterExample: %v", err)
	}
	if result == nil {
		t.Fatal("runHonshuGroundwaterExample returned nil result")
	}
	if result.RequestID == "" {
		t.Fatal("result RequestID is empty")
	}
	if result.RunnerID == "" {
		t.Fatal("result RunnerID is empty")
	}
	if result.ArtifactsDir != artifactsDir {
		t.Fatalf("ArtifactsDir = %q, want %q", result.ArtifactsDir, artifactsDir)
	}
	if want := filepath.Join(artifactsDir, "persistence", "dango.db"); result.PersistencePath != want {
		t.Fatalf("PersistencePath = %q, want %q", result.PersistencePath, want)
	}
	view := result.FinalRunnerView
	if view == nil {
		t.Fatal("result FinalRunnerView is nil")
	}
	if view.RunnerID != result.RunnerID {
		t.Fatalf("view RunnerID = %q, want %q", view.RunnerID, result.RunnerID)
	}
	if view.Phase != runnerpkg.PhaseSettled {
		t.Fatalf("phase = %q, want settled", view.Phase)
	}
	if !strings.Contains(stream.String(), "Orchestrator orchestrator planning started") {
		t.Fatalf("stream missing orchestrator planning status: %s", stream.String())
	}
	if !strings.Contains(stream.String(), "Orchestrator reasoning ·") {
		t.Fatalf("stream missing orchestrator reasoning: %s", stream.String())
	}
	if !strings.Contains(stream.String(), "Runner[") {
		t.Fatalf("stream missing runner updates: %s", stream.String())
	}
	if !strings.Contains(stream.String(), "phase=settled") {
		t.Fatalf("stream missing settled update: %s", stream.String())
	}
	if strings.Contains(stream.String(), "sample after two days of rain") {
		t.Fatalf("stream leaked raw snapshot payload: %s", stream.String())
	}
	for _, noisy := range []string{" tool=", "event=tool_result", "total_tokens="} {
		if strings.Contains(stream.String(), noisy) {
			t.Fatalf("stream contains low-level noise %q:\n%s", noisy, stream.String())
		}
	}
	if _, err := os.Stat(filepath.Join(artifactsDir, "stream_events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("root stream_events.jsonl err = %v, want not exist", err)
	}
	events := readStreamEvents(t, filepath.Join(artifactsDir, "debug", "stream_events.jsonl"))
	if !hasPlannerOutputEvent(events) {
		t.Fatalf("stream event log missing completed orchestrator planner output")
	}
	exchanges, err := filepath.Glob(filepath.Join(artifactsDir, "exchanges", "*.md"))
	if err != nil {
		t.Fatalf("glob exchanges: %v", err)
	}
	if len(exchanges) == 0 {
		t.Fatal("expected exchange markdown files under artifacts/exchanges")
	}
	for _, want := range []string{"submitting request to orchestrator", "runner created", "runner settled"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs missing %q:\n%s", want, logs.String())
		}
	}
	for _, want := range []string{"request persisted", "describe replay completed", "persistence summaries written"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs missing %q:\n%s", want, logs.String())
		}
	}
	if err := ensureNoPDFSkill(view.Plan); err != nil {
		t.Fatal(err)
	}
	train, err := trainingResultFromView(view, "train_model")
	if err != nil {
		t.Fatalf("trainingResultFromView: %v", err)
	}
	trainDoc, err := exchangeDocumentFromView(view, "train_model")
	if err != nil {
		t.Fatalf("exchangeDocumentFromView: %v", err)
	}
	if len(trainDoc.Resources) != 2 {
		t.Fatalf("train resources = %+v, want CSV and SVG resources", trainDoc.Resources)
	}
	if train.PredictionCount != 36 {
		t.Fatalf("prediction count = %d, want 36", train.PredictionCount)
	}
	if _, err := os.Stat(train.CSVPath); err != nil {
		t.Fatalf("stat CSV: %v", err)
	}
	if _, err := os.Stat(train.PlotPath); err != nil {
		t.Fatalf("stat plot: %v", err)
	}
	csvData, err := os.ReadFile(train.CSVPath)
	if err != nil {
		t.Fatalf("read CSV: %v", err)
	}
	if !strings.Contains(string(csvData), "predicted_water_level_m_bgl") {
		t.Fatalf("CSV missing prediction header: %s", string(csvData))
	}
}

func TestRunHonshuGroundwaterExampleWritesPersistenceSummaries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := newFakeLLMClient(t)
	artifactsDir := t.TempDir()
	logger, err := newExampleLogger(io.Discard, "debug")
	if err != nil {
		t.Fatalf("newExampleLogger: %v", err)
	}

	result, err := runHonshuGroundwaterExample(ctx, exampleConfig{
		MeasurementsJSON: embeddedSampleMeasurements,
		ArtifactsDir:     artifactsDir,
		Out:              io.Discard,
		Logger:           logger,
		LLMClient:        client,
	})
	if err != nil {
		t.Fatalf("runHonshuGroundwaterExample: %v", err)
	}

	var describe describeViewSummary
	readJSONFile(t, filepath.Join(artifactsDir, "debug", "describe_view.json"), &describe)
	if describe.RequestID != result.RequestID {
		t.Fatalf("describe request_id = %q, want %q", describe.RequestID, result.RequestID)
	}
	if describe.RunnerID != result.RunnerID {
		t.Fatalf("describe runner_id = %q, want %q", describe.RunnerID, result.RunnerID)
	}
	if describe.Phase != runnerpkg.PhaseSettled {
		t.Fatalf("describe phase = %q, want %q", describe.Phase, runnerpkg.PhaseSettled)
	}
	if describe.NodeCount != 2 {
		t.Fatalf("describe node_count = %d, want 2", describe.NodeCount)
	}
	if describe.ArtifactCount == 0 {
		t.Fatal("describe artifact_count = 0, want persisted artifacts")
	}
	if describe.LatestEventSequence == 0 {
		t.Fatal("describe latest_event_sequence = 0, want persisted replay cursor")
	}
	if !hasDescribeNode(describe.Nodes, "enrich_elevation", "elevation_lookup") {
		t.Fatalf("describe nodes missing enrich_elevation: %+v", describe.Nodes)
	}
	if !hasDescribeNode(describe.Nodes, "train_model", "train_gp_model") {
		t.Fatalf("describe nodes missing train_model: %+v", describe.Nodes)
	}

	var records runnerRecordsSummary
	readJSONFile(t, filepath.Join(artifactsDir, "debug", "runner_records.json"), &records)
	if records.RecordCount == 0 {
		t.Fatal("runner_records record_count = 0, want persisted records")
	}
	if !hasRunnerRecordSummaryEvent(records.Records, runnerpkg.EventNodeCompleted.String(), "train_model") {
		t.Fatalf("runner_records missing completed train_model event: %+v", records.Records)
	}

	var summary persistenceSummary
	readJSONFile(t, filepath.Join(artifactsDir, "debug", "persistence_summary.json"), &summary)
	if summary.RequestID != result.RequestID {
		t.Fatalf("summary request_id = %q, want %q", summary.RequestID, result.RequestID)
	}
	if summary.RunnerID != result.RunnerID {
		t.Fatalf("summary runner_id = %q, want %q", summary.RunnerID, result.RunnerID)
	}
	if summary.PersistencePath != result.PersistencePath {
		t.Fatalf("summary persistence_path = %q, want %q", summary.PersistencePath, result.PersistencePath)
	}
	if summary.StreamEventsPath != filepath.Join(artifactsDir, "debug", "stream_events.jsonl") {
		t.Fatalf("summary stream_events_path = %q", summary.StreamEventsPath)
	}
	if summary.DescribeViewPath != filepath.Join(artifactsDir, "debug", "describe_view.json") {
		t.Fatalf("summary describe_view_path = %q", summary.DescribeViewPath)
	}
	if summary.RunnerRecordsPath != filepath.Join(artifactsDir, "debug", "runner_records.json") {
		t.Fatalf("summary runner_records_path = %q", summary.RunnerRecordsPath)
	}
	if summary.DescribeNodeCount != describe.NodeCount {
		t.Fatalf("summary describe_node_count = %d, want %d", summary.DescribeNodeCount, describe.NodeCount)
	}
	if summary.DescribeArtifactCount != describe.ArtifactCount {
		t.Fatalf("summary describe_artifact_count = %d, want %d", summary.DescribeArtifactCount, describe.ArtifactCount)
	}
	if summary.RunnerRecordCount != records.RecordCount {
		t.Fatalf("summary runner_record_count = %d, want %d", summary.RunnerRecordCount, records.RecordCount)
	}
	if summary.LatestEventSequence != describe.LatestEventSequence {
		t.Fatalf("summary latest_event_sequence = %d, want %d", summary.LatestEventSequence, describe.LatestEventSequence)
	}
	if summary.Cursor.EventSequence != describe.LatestEventSequence {
		t.Fatalf("summary cursor event_sequence = %d, want %d", summary.Cursor.EventSequence, describe.LatestEventSequence)
	}
	if summary.Cursor.RunnerID != result.RunnerID {
		t.Fatalf("summary cursor runner_id = %q, want %q", summary.Cursor.RunnerID, result.RunnerID)
	}
	rawSummary := map[string]any{}
	readJSONFile(t, filepath.Join(artifactsDir, "debug", "persistence_summary.json"), &rawSummary)
	rawCursor, ok := rawSummary["cursor"].(map[string]any)
	if !ok {
		t.Fatalf("summary cursor raw JSON = %#v, want object", rawSummary["cursor"])
	}
	for _, key := range []string{"request_id", "runner_id", "event_sequence"} {
		if _, ok := rawCursor[key]; !ok {
			t.Fatalf("summary cursor missing snake_case key %q: %#v", key, rawCursor)
		}
	}
	for _, key := range []string{"RequestID", "RunnerID", "EventSequence"} {
		if _, ok := rawCursor[key]; ok {
			t.Fatalf("summary cursor contains camel-case key %q: %#v", key, rawCursor)
		}
	}
}

func TestRunHonshuGroundwaterExamplePersistsTerminalRequestState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := newFakeLLMClient(t)
	artifactsDir := t.TempDir()
	logger, err := newExampleLogger(io.Discard, "debug")
	if err != nil {
		t.Fatalf("newExampleLogger: %v", err)
	}

	result, err := runHonshuGroundwaterExample(ctx, exampleConfig{
		MeasurementsJSON: embeddedSampleMeasurements,
		ArtifactsDir:     artifactsDir,
		Out:              io.Discard,
		Logger:           logger,
		LLMClient:        client,
	})
	if err != nil {
		t.Fatalf("runHonshuGroundwaterExample: %v", err)
	}
	if _, err := os.Stat(result.PersistencePath); err != nil {
		t.Fatalf("stat persistence db: %v", err)
	}

	reopened, err := runtimepkg.Open(runtimepkg.Config{SQLitePath: result.PersistencePath})
	if err != nil {
		t.Fatalf("runtime.Open(reopen): %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Fatalf("Close(reopened persistence): %v", err)
		}
	}()

	if err := waitForPersistedTerminalRunnerState(ctx, reopened.EventLogStore(), result.RequestID, result.RunnerID); err != nil {
		t.Fatalf("waitForPersistedTerminalRunnerState: %v", err)
	}
	rawEvents, err := reopened.EventLogStore().LoadEvents(ctx, streampkg.Scope{RequestID: result.RequestID}, 1, streampkg.Filter{})
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	if len(rawEvents) == 0 {
		t.Fatal("persisted request event log is empty")
	}
	if !hasPersistedTerminalRunnerState(rawEvents, result.RunnerID) {
		t.Fatalf("persisted request event log missing terminal settled phase for %q", result.RunnerID)
	}
}

func TestRunHonshuGroundwaterExampleReopensPersistedState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := newFakeLLMClient(t)
	artifactsDir := t.TempDir()
	logger, err := newExampleLogger(io.Discard, "debug")
	if err != nil {
		t.Fatalf("newExampleLogger: %v", err)
	}

	result, err := runHonshuGroundwaterExample(ctx, exampleConfig{
		MeasurementsJSON: embeddedSampleMeasurements,
		ArtifactsDir:     artifactsDir,
		Out:              io.Discard,
		Logger:           logger,
		LLMClient:        client,
	})
	if err != nil {
		t.Fatalf("runHonshuGroundwaterExample: %v", err)
	}

	reopened, err := runtimepkg.Open(runtimepkg.Config{SQLitePath: result.PersistencePath})
	if err != nil {
		t.Fatalf("runtime.Open(reopen): %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Fatalf("Close(reopened persistence): %v", err)
		}
	}()

	fresh := orchestrate.NewOrchestrator(
		orchestrate.WithOrchestratorContext(ctx),
		orchestrate.WithPersistence(reopened.Backend()),
	)
	rawEvents, err := reopened.EventLogStore().LoadEvents(ctx, streampkg.Scope{RequestID: result.RequestID}, 1, streampkg.Filter{})
	if err != nil {
		t.Fatalf("LoadEvents(reopen): %v", err)
	}
	if len(rawEvents) == 0 {
		t.Fatal("LoadEvents(reopen) returned no persisted request frames")
	}
	if !hasPersistedTerminalRunnerState(rawEvents, result.RunnerID) {
		t.Fatalf("LoadEvents(reopen) missing terminal settled phase for %q", result.RunnerID)
	}
	records, err := fresh.LoadRunnerRecords(ctx, result.RunnerID)
	if err != nil {
		t.Fatalf("LoadRunnerRecords(reopen): %v", err)
	}
	if len(records) == 0 {
		t.Fatal("LoadRunnerRecords(reopen) returned no records")
	}
	if !hasPersistedRunnerRecord(records, runnerpkg.EventNodeCompleted.String(), "train_model") {
		t.Fatalf("reopened runner records missing completed train_model event: %+v", records)
	}
	view, err := fresh.DescribeRequest(ctx, result.RequestID)
	if err != nil {
		t.Fatalf("DescribeRequest(reopen): %v", err)
	}
	if view.RunnerID != result.RunnerID {
		t.Fatalf("DescribeRequest(reopen) runner_id = %q, want %q", view.RunnerID, result.RunnerID)
	}
	if view.Phase != runnerpkg.PhaseSettled {
		t.Fatalf("DescribeRequest(reopen) phase = %q, want %q", view.Phase, runnerpkg.PhaseSettled)
	}
	if view.LatestEventSequence == 0 {
		t.Fatal("DescribeRequest(reopen) latest_event_sequence = 0, want replay cursor")
	}
	cursor, err := reopened.SnapshotCursorStore().LoadCursor(ctx, result.RequestID)
	if err != nil {
		t.Fatalf("LoadCursor(reopen): %v", err)
	}
	if cursor.RunnerID != result.RunnerID {
		t.Fatalf("LoadCursor(reopen) runner_id = %q, want %q", cursor.RunnerID, result.RunnerID)
	}
	if cursor.EventSequence != view.SnapshotCursor().EventSequence {
		t.Fatalf("LoadCursor(reopen) event_sequence = %d, want %d", cursor.EventSequence, view.SnapshotCursor().EventSequence)
	}
}

func TestWaitForPersistedTerminalRunnerStateLoadsOnlyNewEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store := &recordingEventLogStore{
		loads: [][]streampkg.Event{
			{{
				SequenceNumber: 1,
				EventType:      streampkg.EventStatusProgress,
				Scope:          streampkg.Scope{RequestID: "req-1", RunnerID: "runner-1"},
			}},
			{{
				SequenceNumber: 2,
				EventType:      streampkg.EventRunnerPhaseChanged,
				Scope:          streampkg.Scope{RequestID: "req-1", RunnerID: "runner-1"},
				Delta:          json.RawMessage(`{"phase":"settled","status":"idle"}`),
			}},
		},
	}
	if err := waitForPersistedTerminalRunnerState(ctx, store, "req-1", "runner-1"); err != nil {
		t.Fatalf("waitForPersistedTerminalRunnerState: %v", err)
	}
	want := []uint64{1, 2}
	if len(store.froms) != len(want) {
		t.Fatalf("LoadEvents called with %v, want %v", store.froms, want)
	}
	for i, got := range store.froms {
		if got != want[i] {
			t.Fatalf("LoadEvents from[%d] = %d, want %d", i, got, want[i])
		}
	}
}

type recordingEventLogStore struct {
	loads [][]streampkg.Event
	froms []uint64
	call  int
}

func (s *recordingEventLogStore) AppendEvent(ctx context.Context, event streampkg.Event) error {
	return nil
}

func (s *recordingEventLogStore) LoadEvents(ctx context.Context, scope streampkg.Scope, from uint64, filter streampkg.Filter) ([]streampkg.Event, error) {
	s.froms = append(s.froms, from)
	if s.call >= len(s.loads) {
		s.call++
		return nil, nil
	}
	events := append([]streampkg.Event(nil), s.loads[s.call]...)
	s.call++
	return events, nil
}

var _ storepkg.EventLogStore = (*recordingEventLogStore)(nil)

func readStreamEvents(t *testing.T, path string) []streampkg.Event {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open stream events: %v", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var events []streampkg.Event
	for {
		var event streampkg.Event
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode stream event: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func readJSONFile(t *testing.T, path string, dst any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("unmarshal %s: %v\n%s", path, err, string(data))
	}
}

func hasDescribeNode(nodes []describeNodeSummary, nodeID string, skillName string) bool {
	for _, node := range nodes {
		if node.ID == nodeID && node.SkillName == skillName {
			return true
		}
	}
	return false
}

func hasRunnerRecordSummaryEvent(records []runnerRecordSummaryEntry, eventType string, nodeID string) bool {
	for _, record := range records {
		if record.EventType == eventType && record.NodeID == nodeID {
			return true
		}
	}
	return false
}

func hasPersistedRunnerRecord(records []runnerpkg.RunnerRecord, eventType string, nodeID string) bool {
	for _, record := range records {
		if record.Kind != runnerpkg.RunnerRecordEvent || record.Event == nil {
			continue
		}
		if record.Event.Type == eventType && record.Event.NodeID == nodeID {
			return true
		}
	}
	return false
}

func hasPlannerOutputEvent(events []streampkg.Event) bool {
	for _, event := range events {
		if event.EventType != streampkg.EventLLMOutputDelta || event.From.Layer != "orchestrator" || event.Status != streampkg.StatusCompleted {
			continue
		}
		var text string
		if err := json.Unmarshal(event.Delta, &text); err != nil {
			continue
		}
		if strings.Contains(text, `"plan"`) && strings.Contains(text, "train_gp_model") {
			return true
		}
	}
	return false
}

func TestHonshuOrchestratorRegistersAutonomousSkillRuntimes(t *testing.T) {
	root, err := exampleRoot()
	if err != nil {
		t.Fatalf("exampleRoot: %v", err)
	}
	client := &llm.Client{}
	o := orchestrate.NewOrchestrator(orchestrate.WithOrchestratorContext(context.Background()))
	if err := o.SetClient(client); err != nil {
		t.Fatalf("SetClient: %v", err)
	}
	if err := o.AddSkillDirs(llm.ConversationConfig{MaxSteps: 32},
		filepath.Join(root, "elevation_lookup"),
		filepath.Join(root, "train_gp_model"),
		filepath.Join(root, "markdown_to_pdf"),
	); err != nil {
		t.Fatalf("AddSkillDirs: %v", err)
	}

	for _, skillName := range []string{"elevation_lookup", "train_gp_model", "markdown_to_pdf"} {
		sk := o.Skills()[skillName]
		if sk == nil {
			t.Fatalf("skill %q was not registered", skillName)
		}
		bound, err := sk.Bind(client, llm.DefaultConversationConfig())
		if err != nil {
			t.Fatalf("bind %s: %v", skillName, err)
		}
		tools := map[string]bool{}
		for _, spec := range bound.Conversation().Tools() {
			tools[spec.Name] = true
		}
		for _, name := range []string{"bash", "read_file", "write_file", "grep", "pwd"} {
			if !tools[name] {
				t.Fatalf("%s missing runtime tool %q: %v", skillName, name, tools)
			}
		}
		for _, name := range []string{"lookup_elevations", "train_gp_model", "render_markdown_pdf"} {
			if tools[name] {
				t.Fatalf("%s should not expose example-owned domain tool %q: %v", skillName, name, tools)
			}
		}
		instructions := bound.Conversation().Instructions()
		for _, want := range []string{"Workspace access:", "Temp playground:", "Relative file paths and shell commands run here"} {
			if !strings.Contains(instructions, want) {
				t.Fatalf("%s instructions missing %q:\n%s", skillName, want, instructions)
			}
		}
	}
}

func TestNewExampleLoggerRejectsInvalidLevel(t *testing.T) {
	if _, err := newExampleLogger(io.Discard, "verbose"); err == nil {
		t.Fatal("newExampleLogger accepted invalid log level")
	}
}

func TestStreamRendererIncludesFailureContext(t *testing.T) {
	line := exampleRenderLine(streampkg.Event{
		EventType: streampkg.EventRunnerNodeFailed,
		From:      streampkg.Source{Layer: "runner", ID: "runner-1"},
		Status:    streampkg.StatusFailed,
		Scope:     streampkg.Scope{RunnerID: "runner-1", NodeID: "train_model"},
		Delta:     json.RawMessage(`{"event":"NodeFailed","node_id":"train_model","error":"skill execution loop did not produce final markdown"}`),
		Metadata:  map[string]any{"skill_name": "train_gp_model"},
	})
	for _, want := range []string{
		"status=failed",
		"error=\"skill execution loop did not produce final markdown\"",
		"event=node.failed",
		"node=train_model",
		"skill=train_gp_model",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("compact line missing %q:\n%s", want, line)
		}
	}
}

func TestStreamRendererShowsSettledPhase(t *testing.T) {
	line := exampleRenderLine(streampkg.Event{
		EventType: streampkg.EventRunnerPhaseChanged,
		From:      streampkg.Source{Layer: "runner", ID: "runner-1"},
		Status:    streampkg.StatusCompleted,
		Scope:     streampkg.Scope{RunnerID: "runner-1"},
		Delta:     json.RawMessage(`{"phase":"settled","status":"idle"}`),
	})
	for _, want := range []string{"Runner[runner-1]", "status=idle", "phase=settled"} {
		if !strings.Contains(line, want) {
			t.Fatalf("compact line missing %q:\n%s", want, line)
		}
	}
}

func TestStreamRendererShowsToolExecution(t *testing.T) {
	line := exampleRenderLine(streampkg.Event{
		EventType: streampkg.EventToolExecutionStarted,
		From:      streampkg.Source{Layer: "skill", ID: "train_gp_model"},
		Status:    streampkg.StatusRunning,
		Scope:     streampkg.Scope{NodeID: "train_model"},
		Delta:     json.RawMessage(`{"call_id":"call_1","name":"bash"}`),
		Metadata:  map[string]any{"skill_name": "train_gp_model"},
	})
	for _, want := range []string{"Skill[train_gp_model]", "tool calling", "bash", "|"} {
		if !strings.Contains(line, want) {
			t.Fatalf("compact line missing %q:\n%s", want, line)
		}
	}
}

func TestStreamRendererShowsFailedToolExecution(t *testing.T) {
	line := exampleRenderLine(streampkg.Event{
		EventType: streampkg.EventToolExecutionFailed,
		From:      streampkg.Source{Layer: "skill", ID: "train_gp_model"},
		Status:    streampkg.StatusFailed,
		Scope:     streampkg.Scope{NodeID: "train_model"},
		Delta:     json.RawMessage(`{"call_id":"call_1","name":"bash","error":"exit status 1"}`),
	})
	for _, want := range []string{"Skill[train_gp_model]", "tool failed", "bash", "status=failed", "skill=train_gp_model", "error=\"exit status 1\""} {
		if !strings.Contains(line, want) {
			t.Fatalf("compact line missing %q:\n%s", want, line)
		}
	}
}

func exampleRenderLine(event streampkg.Event) string {
	return streamrender.New(nil, streamrender.Config{}).FormatEvent(event)
}

func runSkillScript(ctx context.Context, skillDir string, script string, inputJSON string) (string, error) {
	cmd := exec.CommandContext(ctx, "uv", "run", "--quiet", "python", script)
	cmd.Dir = skillDir
	cmd.Env = append(os.Environ(),
		"UV_CACHE_DIR="+filepath.Join(os.TempDir(), "dango-uv-cache"),
		"UV_PYTHON_DOWNLOADS=never",
	)
	cmd.Stdin = strings.NewReader(inputJSON)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run %s in %s: %w\n%s", script, skillDir, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

type trainingResult struct {
	Model              string  `json:"model"`
	ObservationCount   int     `json:"observation_count"`
	PredictionCount    int     `json:"prediction_count"`
	CSVPath            string  `json:"csv_path"`
	PlotPath           string  `json:"plot_path"`
	MeanPredictedMBGL  float64 `json:"mean_predicted_water_level_m_bgl"`
	ValidationSummary  string  `json:"validation_summary"`
	DownstreamReminder string  `json:"downstream_reminder"`
}

func trainingResultFromView(view *runnerpkg.RunnerView, nodeID string) (*trainingResult, error) {
	doc, err := exchangeDocumentFromView(view, nodeID)
	if err != nil {
		return nil, err
	}
	jsonText, err := extractJSONBlock(doc.Handoff)
	if err != nil {
		return nil, err
	}
	var result trainingResult
	if err := json.Unmarshal([]byte(jsonText), &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func exchangeDocumentFromView(view *runnerpkg.RunnerView, nodeID string) (*runnerpkg.ExchangeDocument, error) {
	raw, ok := view.Snapshot.CompletedNodes[nodeID].(string)
	if !ok || raw == "" {
		return nil, fmt.Errorf("missing completed output for node %q", nodeID)
	}
	doc, err := runnerpkg.ParseExchangeMarkdown(raw)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func ensureNoPDFSkill(plan *orchestrate.CoarsePlan) error {
	if plan == nil {
		return fmt.Errorf("missing plan")
	}
	for _, node := range plan.Nodes {
		if node.SkillName == "markdown_to_pdf" {
			return fmt.Errorf("distractor markdown_to_pdf skill was selected")
		}
	}
	return nil
}

func extractJSONBlock(text string) (string, error) {
	if start := strings.Index(text, "```json"); start >= 0 {
		rest := text[start+len("```json"):]
		if end := strings.Index(rest, "```"); end >= 0 {
			return strings.TrimSpace(rest[:end]), nil
		}
	}
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end > start {
			return strings.TrimSpace(text[start : end+1]), nil
		}
	}
	return "", fmt.Errorf("no JSON object found")
}

type responsesRequest struct {
	Model        string          `json:"model"`
	Input        json.RawMessage `json:"input"`
	Instructions string          `json:"instructions"`
	Stream       bool            `json:"stream"`
}

type plannerPrompt struct {
	Mode string `json:"mode"`
	Data struct {
		Request string `json:"request"`
		Skills  []struct {
			Name string `json:"name"`
		} `json:"skills"`
	} `json:"data"`
}

func newFakeLLMClient(t *testing.T) *llm.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(serveFakeLLM))
	t.Cleanup(server.Close)
	raw := openai.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(server.URL+"/"),
	)
	client, err := llm.NewClient(llm.ProviderOpenAI, "honshu-groundwater-test", raw, llm.DefaultClientConfig())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func serveFakeLLM(w http.ResponseWriter, r *http.Request) {
	req, err := decodeResponsesRequest(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userText, err := lastUserText(req.Input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var prompt plannerPrompt
	if err := json.Unmarshal([]byte(userText), &prompt); err == nil && prompt.Mode != "" {
		if req.Stream {
			serveFakePlannerStream(w, req, prompt)
			return
		}
		serveFakePlanner(w, req, prompt)
		return
	}
	serveFakeSkill(w, req, userText, req.Instructions)
}

func serveFakePlannerStream(w http.ResponseWriter, req *responsesRequest, prompt plannerPrompt) {
	if prompt.Mode != "plan" {
		respondStreamText(w, req.Model, mustJSON(map[string]any{"approved": true}))
		return
	}
	respondStreamText(w, req.Model, mustJSON(map[string]any{"plan": groundwaterPlan(prompt.Data.Request)}))
}

func serveFakePlanner(w http.ResponseWriter, req *responsesRequest, prompt plannerPrompt) {
	switch prompt.Mode {
	case "plan":
		if missing := missingSkills(prompt, "elevation_lookup", "train_gp_model"); len(missing) > 0 {
			respondText(w, req.Model, mustJSON(map[string]any{"reject": orchestrate.RejectReason{
				Summary:       "required skills are missing",
				Analysis:      "Honshu groundwater modeling requires elevation enrichment and GP modeling skills",
				MissingSkills: missing,
			}}))
			return
		}
		respondText(w, req.Model, mustJSON(map[string]any{"plan": groundwaterPlan(prompt.Data.Request)}))
	case "review":
		respondText(w, req.Model, mustJSON(map[string]any{"approved": true}))
	case "replan":
		respondText(w, req.Model, mustJSON(map[string]any{"plan": groundwaterPlan(prompt.Data.Request)}))
	default:
		http.Error(w, "unknown planner mode", http.StatusBadRequest)
	}
}

func serveFakeSkill(w http.ResponseWriter, req *responsesRequest, userText string, instructions string) {
	if strings.HasPrefix(userText, "Polish the assigned task plan") {
		doc, err := polishExchangeMarkdown(userText)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondMaybeText(w, req, doc)
		return
	}
	if strings.HasPrefix(userText, "Summarize this executor output") {
		doc, err := reportExchangeMarkdown(userText)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondMaybeText(w, req, doc)
		return
	}
	if output := lastFunctionCallOutput(req.Input); output != "" {
		doc, err := executionExchangeMarkdown(output)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondMaybeText(w, req, doc)
		return
	}

	switch {
	case strings.Contains(userText, "Train a GP-style groundwater model"):
		skillDir, err := sourceWorkspaceFromPrompt(instructions)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		artifactsRoot, err := artifactsRootFromPrompt(userText)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		command, err := skillScriptCommand(skillDir, "scripts/train.py", map[string]string{
			"parent_exchange": userText,
			"artifacts_dir":   filepath.Join(artifactsRoot, "train_gp_model"),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondMaybeToolCall(w, req, "call_train_gp", "bash", map[string]any{
			"command":         command,
			"timeout_seconds": 120,
		})
	case strings.Contains(userText, "enrich every Honshu observation"):
		raw, err := extractJSONBlock(userText)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		skillDir, err := sourceWorkspaceFromPrompt(instructions)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		command, err := skillScriptCommand(skillDir, "scripts/enrich.py", map[string]string{
			"observations_json": raw,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondMaybeToolCall(w, req, "call_lookup_elevation", "bash", map[string]any{
			"command":         command,
			"timeout_seconds": 60,
		})
	default:
		skillDir, err := sourceWorkspaceFromPrompt(instructions)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		command, err := skillScriptCommand(skillDir, "scripts/render.py", map[string]string{
			"markdown": userText,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondMaybeToolCall(w, req, "call_render_pdf", "bash", map[string]any{
			"command":         command,
			"timeout_seconds": 60,
		})
	}
}

func sourceWorkspaceFromPrompt(prompt string) (string, error) {
	const marker = "- Source workspace: "
	start := strings.Index(prompt, marker)
	if start < 0 {
		return "", fmt.Errorf("prompt does not include source workspace")
	}
	rest := prompt[start+len(marker):]
	end := strings.Index(rest, ". Prefer")
	if end < 0 {
		return "", fmt.Errorf("source workspace line is malformed")
	}
	return strings.TrimSpace(rest[:end]), nil
}

func artifactsRootFromPrompt(prompt string) (string, error) {
	const marker = "Artifacts root:\n"
	start := strings.Index(prompt, marker)
	if start < 0 {
		return "", fmt.Errorf("prompt does not include artifacts root")
	}
	rest := prompt[start+len(marker):]
	line, _, _ := strings.Cut(rest, "\n")
	root := strings.TrimSpace(line)
	if root == "" {
		return "", fmt.Errorf("artifacts root is empty")
	}
	return root, nil
}

func skillScriptCommand(skillDir string, script string, payload any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("UV_CACHE_DIR=%s UV_PYTHON_DOWNLOADS=never uv --directory %s run --quiet python %s <<'DANGO_JSON'\n%s\nDANGO_JSON",
		shellQuote(filepath.Join(os.TempDir(), "dango-uv-cache")),
		shellQuote(skillDir),
		shellQuote(script),
		string(data),
	), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func findTool(t *testing.T, tools []llm.Tool, name string) llm.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func polishExchangeMarkdown(prompt string) (string, error) {
	doc := runnerpkg.ExchangeDocument{
		Stage: runnerpkg.ExchangeStagePolish,
		Handoffs: []runnerpkg.ExchangeHandoff{{
			To:      runnerpkg.ExchangeRecipientOrchestrator,
			Intent:  runnerpkg.ExchangeIntentReview,
			Summary: "Skill-specific execution plan is ready for review.",
		}},
		Memo:      "The skill reviewed its assigned task without running execution tools.",
		Reasoning: "The polished plan keeps execution concerns scoped to this skill.",
		Handoff:   "Proceed with this skill only if the assigned task matches its stated responsibility.\n\n" + prompt,
	}
	return doc.Markdown()
}

func groundwaterPlan(request string) *orchestrate.CoarsePlan {
	return &orchestrate.CoarsePlan{
		Request: request,
		Nodes: []orchestrate.CoarsePlanNode{
			{
				ID:              "enrich_elevation",
				SkillName:       "elevation_lookup",
				TaskDescription: "Parse the supplied messy groundwater JSON and enrich every Honshu observation with elevation.\n\nOriginal request:\n" + request,
			},
			{
				ID:              "train_model",
				SkillName:       "train_gp_model",
				TaskDescription: "Train a GP-style groundwater model from enriched observations and write prediction CSV plus a plot artifact.",
				DependsOn:       []string{"enrich_elevation"},
			},
		},
	}
}

func missingSkills(prompt plannerPrompt, required ...string) []string {
	available := make(map[string]struct{}, len(prompt.Data.Skills))
	for _, skill := range prompt.Data.Skills {
		available[skill.Name] = struct{}{}
	}
	var missing []string
	for _, name := range required {
		if _, ok := available[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func executionExchangeMarkdown(toolOutput string) (string, error) {
	doc := runnerpkg.ExchangeDocument{
		Stage: runnerpkg.ExchangeStageExecute,
		Handoffs: []runnerpkg.ExchangeHandoff{{
			To:      runnerpkg.ExchangeRecipientDownstream,
			Intent:  runnerpkg.ExchangeIntentContinue,
			Summary: "Structured tool output for downstream skills.",
		}},
		Memo:      "The skill used its required domain tool and captured structured output.",
		Reasoning: "The tool output is the authoritative handoff for the next node.",
		Handoff:   fencedJSON(toolOutput),
		Resources: exchangeResourcesFromToolOutput(toolOutput),
	}
	return doc.Markdown()
}

func exchangeResourcesFromToolOutput(toolOutput string) []runnerpkg.ExchangeResource {
	var payload struct {
		Resources []runnerpkg.ExchangeResource `json:"resources"`
	}
	if err := json.Unmarshal([]byte(toolOutput), &payload); err != nil {
		return nil
	}
	return payload.Resources
}

func reportExchangeMarkdown(output string) (string, error) {
	doc := runnerpkg.ExchangeDocument{
		Stage: runnerpkg.ExchangeStageReport,
		Handoffs: []runnerpkg.ExchangeHandoff{{
			To:      runnerpkg.ExchangeRecipientOrchestrator,
			Intent:  runnerpkg.ExchangeIntentSummarize,
			Summary: "Report summary for final request synthesis.",
		}},
		Memo:      "Report created from the executor output.",
		Reasoning: "The report keeps artifact references visible to the orchestrator.",
		Handoff:   summarizeReportOutput(output),
	}
	return doc.Markdown()
}

func decodeResponsesRequest(r io.Reader) (*responsesRequest, error) {
	var req responsesRequest
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		return nil, err
	}
	if req.Model == "" {
		return nil, errMissingModel
	}
	if len(req.Input) == 0 {
		return nil, errMissingInput
	}
	return &req, nil
}

var (
	errMissingModel = &fakeLLMError{"missing model"}
	errMissingInput = &fakeLLMError{"missing input"}
)

type fakeLLMError struct {
	msg string
}

func (e *fakeLLMError) Error() string {
	return e.msg
}

func lastUserText(raw json.RawMessage) (string, error) {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString, nil
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", err
	}
	for i := len(items) - 1; i >= 0; i-- {
		role, _ := items[i]["role"].(string)
		if role != "" && role != "user" {
			continue
		}
		if text, ok := textFromContent(items[i]["content"]); ok {
			return text, nil
		}
		if text, ok := items[i]["text"].(string); ok && text != "" {
			return text, nil
		}
	}
	return "", &fakeLLMError{"responses input has no user text"}
}

func textFromContent(content any) (string, bool) {
	switch value := content.(type) {
	case string:
		return value, value != ""
	case []any:
		var parts []string
		for _, item := range value {
			if part, ok := item.(map[string]any); ok {
				if text, ok := part["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n"), true
		}
	}
	return "", false
}

func lastFunctionCallOutput(raw json.RawMessage) string {
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return ""
	}
	for i := len(items) - 1; i >= 0; i-- {
		if typ, _ := items[i]["type"].(string); typ != "function_call_output" {
			continue
		}
		if output, ok := items[i]["output"].(string); ok {
			return output
		}
	}
	return ""
}

func respondText(w http.ResponseWriter, model, text string) {
	writeJSON(w, map[string]any{
		"id":         "resp_text",
		"object":     "response",
		"created_at": 0,
		"model":      model,
		"status":     "completed",
		"output": []map[string]any{{
			"id":     "msg_text",
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			}},
		}},
		"parallel_tool_calls": false,
		"tool_choice":         "auto",
		"tools":               []any{},
	})
}

func respondStreamText(w http.ResponseWriter, model, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, event := range []string{
		reasoningDeltaEvent("Planning with groundwater and elevation skills."),
		textDeltaEvent(text),
		completedEvent(model, text),
	} {
		_, _ = w.Write([]byte(event))
		if !strings.HasSuffix(event, "\n\n") {
			_, _ = w.Write([]byte("\n\n"))
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func respondMaybeText(w http.ResponseWriter, req *responsesRequest, text string) {
	if req.Stream {
		respondStreamText(w, req.Model, text)
		return
	}
	respondText(w, req.Model, text)
}

func reasoningDeltaEvent(delta string) string {
	return fmt.Sprintf(
		"event: response.reasoning_summary_text.delta\n"+
			"data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":%q,\"item_id\":\"r1\",\"output_index\":0,\"content_index\":0,\"sequence_number\":0}",
		delta,
	)
}

func textDeltaEvent(delta string) string {
	return fmt.Sprintf(
		"event: response.output_text.delta\n"+
			"data: {\"type\":\"response.output_text.delta\",\"delta\":%q,\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"sequence_number\":1}",
		delta,
	)
}

func completedEvent(model, text string) string {
	data := fmt.Sprintf(
		`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_stream","object":"response","created_at":0,"model":%q,"status":"completed","output":[{"id":"msg_stream","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}],"parallel_tool_calls":false,"tool_choice":"auto","tools":[],"usage":{"input_tokens":3,"input_tokens_details":{"cached_tokens":0},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":7}}}`,
		model,
		text,
	)
	return "event: response.completed\ndata: " + data
}

func respondMaybeToolCall(w http.ResponseWriter, req *responsesRequest, callID, name string, args map[string]any) {
	if req.Stream {
		respondStreamToolCall(w, req.Model, callID, name, args)
		return
	}
	respondToolCall(w, req.Model, callID, name, args)
}

func respondStreamToolCall(w http.ResponseWriter, model, callID, name string, args map[string]any) {
	argBytes, _ := json.Marshal(args)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	data := fmt.Sprintf(
		`{"type":"response.completed","sequence_number":1,"response":{"id":"resp_tool_stream","object":"response","created_at":0,"model":%q,"status":"completed","output":[{"id":%q,"type":"function_call","status":"completed","call_id":%q,"name":%q,"arguments":%q}],"parallel_tool_calls":false,"tool_choice":"auto","tools":[],"usage":{"input_tokens":3,"input_tokens_details":{"cached_tokens":0},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":7}}}`,
		model,
		"fc_"+name,
		callID,
		name,
		string(argBytes),
	)
	_, _ = w.Write([]byte("event: response.completed\ndata: " + data + "\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

func respondToolCall(w http.ResponseWriter, model, callID, name string, args map[string]any) {
	argBytes, _ := json.Marshal(args)
	writeJSON(w, map[string]any{
		"id":         "resp_tool",
		"object":     "response",
		"created_at": 0,
		"model":      model,
		"status":     "completed",
		"output": []map[string]any{{
			"id":        "fc_" + name,
			"type":      "function_call",
			"status":    "completed",
			"call_id":   callID,
			"name":      name,
			"arguments": string(argBytes),
		}},
		"parallel_tool_calls": false,
		"tool_choice":         "auto",
		"tools":               []any{},
	})
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func fencedJSON(raw string) string {
	return "```json\n" + strings.TrimSpace(prettyJSON(raw)) + "\n```"
}

func prettyJSON(raw string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(raw), "", "  "); err == nil {
		return buf.String()
	}
	return raw
}

func summarizeReportOutput(output string) string {
	if jsonText, err := extractJSONBlock(output); err == nil {
		return fencedJSON(jsonText)
	}
	trimmed := strings.TrimSpace(output)
	if len(trimmed) > 1200 {
		trimmed = trimmed[:1200] + "\n..."
	}
	return trimmed
}

func mustJSON(v any) string {
	buf, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(buf)
}
