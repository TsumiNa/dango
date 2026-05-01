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
	"github.com/tsumina/dango/internal/llm"
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
	sk, err := llm.NewSkill(filepath.Join(root, "elevation_lookup"), nil, nil)
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
	var stream bytes.Buffer
	var logs bytes.Buffer
	logger, err := newExampleLogger(&logs, "debug")
	if err != nil {
		t.Fatalf("newExampleLogger: %v", err)
	}

	view, err := runHonshuGroundwaterExample(ctx, exampleConfig{
		MeasurementsJSON: embeddedSampleMeasurements,
		ArtifactsDir:     t.TempDir(),
		Out:              &stream,
		Logger:           logger,
		LLMClient:        client,
	})
	if err != nil {
		t.Fatalf("runHonshuGroundwaterExample: %v", err)
	}
	if view.Phase != runnerpkg.PhaseSettled {
		t.Fatalf("phase = %q, want settled", view.Phase)
	}
	if !strings.Contains(stream.String(), "or reasoning: Planning with groundwater and elevation skills.") {
		t.Fatalf("stream missing orchestrator reasoning: %s", stream.String())
	}
	if !strings.Contains(stream.String(), "runner_id=") {
		t.Fatalf("stream missing runner updates: %s", stream.String())
	}
	if !strings.Contains(stream.String(), "phase=settled") {
		t.Fatalf("stream missing settled update: %s", stream.String())
	}
	if strings.Contains(stream.String(), "sample after two days of rain") {
		t.Fatalf("stream leaked raw snapshot payload: %s", stream.String())
	}
	for _, want := range []string{"submitting request to orchestrator", "runner created", "runner settled"} {
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

func TestResolveExampleLLMClientLoadsEnvFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("OPENAI_API_KEY=test-key\nMODEL=test-model\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	client, err := resolveExampleLLMClient(exampleConfig{EnvFiles: []string{envFile}})
	if err != nil {
		t.Fatalf("resolveExampleLLMClient: %v", err)
	}
	if got := client.Model(); got != "test-model" {
		t.Fatalf("model = %q, want test-model", got)
	}
	if got := client.Provider(); got != llm.ProviderOpenAI {
		t.Fatalf("provider = %q, want openai", got)
	}
}

func TestConfigureExampleOrchestratorRegistersAutonomousSkillRuntimes(t *testing.T) {
	root, err := exampleRoot()
	if err != nil {
		t.Fatalf("exampleRoot: %v", err)
	}
	client := &llm.Client{}
	o, err := configureExampleOrchestrator(context.Background(), root, client, nil)
	if err != nil {
		t.Fatalf("configureExampleOrchestrator: %v", err)
	}

	for _, skillName := range []string{"elevation_lookup", "train_gp_model", "markdown_to_pdf"} {
		sk := o.Skills()[skillName]
		if sk == nil {
			t.Fatalf("skill %q was not registered", skillName)
		}
		bound, err := sk.Bind(client, nil, nil)
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

func TestCompactRunnerUpdateIncludesFailureContext(t *testing.T) {
	line := compactRunnerUpdate(runnerpkg.RunnerUpdate{
		RunnerID: "runner-1",
		State: runnerpkg.RunnerState{
			Status: runnerpkg.RunnerStatusFailed,
			Error:  "llm: run exceeded max steps (12) without final response",
		},
		Phase: runnerpkg.PhaseSettled,
		Snapshot: runnerpkg.RunnerSnapshot{
			CompletedNodes: map[string]any{},
			PendingNodes:   map[string]int{"train_model": 0},
		},
		Event: &runnerpkg.RunnerEvent{
			Type:   runnerpkg.EventNodeFailed,
			NodeID: "train_model",
			Data:   "skill execution loop did not produce final markdown",
		},
	})
	for _, want := range []string{
		"status=failed",
		"phase=settled",
		"error=\"llm: run exceeded max steps (12) without final response\"",
		"detail=\"skill execution loop did not produce final markdown\"",
		"event=NodeFailed",
		"node=train_model",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("compact line missing %q:\n%s", want, line)
		}
	}
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
	client, err := llm.NewClient(llm.ClientConfig{
		Provider: llm.ProviderOpenAI,
		Model:    "honshu-groundwater-test",
		Raw:      raw,
	})
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
		respondText(w, req.Model, doc)
		return
	}
	if strings.HasPrefix(userText, "Summarize this executor output") {
		doc, err := reportExchangeMarkdown(userText)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondText(w, req.Model, doc)
		return
	}
	if output := lastFunctionCallOutput(req.Input); output != "" {
		doc, err := executionExchangeMarkdown(output)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		respondText(w, req.Model, doc)
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
		respondToolCall(w, req.Model, "call_train_gp", "bash", map[string]any{
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
		respondToolCall(w, req.Model, "call_lookup_elevation", "bash", map[string]any{
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
		respondToolCall(w, req.Model, "call_render_pdf", "bash", map[string]any{
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
