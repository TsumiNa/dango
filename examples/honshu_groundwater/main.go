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
	"os/exec"
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

type exampleConfig struct {
	MeasurementsJSON string
	ArtifactsDir     string
	Out              io.Writer
	LLMClient        *llm.Client
	EnvFiles         []string
}

type exampleRuntime struct {
	artifactsDir string
	root         string
}

func main() {
	var inputPath string
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	flags.StringVar(&inputPath, "input", "", "path to messy groundwater JSON; uses embedded sample when empty")
	if err := flags.Parse(os.Args[1:]); err != nil {
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := runHonshuGroundwaterExample(ctx, exampleConfig{
		MeasurementsJSON: measurements,
		Out:              os.Stdout,
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

	root, err := exampleRoot()
	if err != nil {
		return nil, err
	}
	runtime := &exampleRuntime{
		artifactsDir: artifactsDir,
		root:         root,
	}
	client, err := resolveExampleLLMClient(cfg)
	if err != nil {
		return nil, err
	}
	orchestrator, err := configureExampleOrchestrator(ctx, root, runtime, client)
	if err != nil {
		return nil, err
	}

	request := buildGroundwaterRequest(cfg.MeasurementsJSON)
	runnerID, err := orchestrator.StartRequest(ctx, &orchestrate.Request{Input: request})
	if err != nil {
		return nil, err
	}
	updates, unsubscribe, err := orchestrator.SubscribeRunner(runnerID, 64)
	if err != nil {
		return nil, err
	}
	defer unsubscribe()
	if err := streamRunnerUpdates(ctx, cfg.Out, updates); err != nil {
		return nil, err
	}

	view, err := orchestrator.WaitRunner(ctx, runnerID)
	if err != nil {
		return nil, err
	}
	if view == nil || view.Phase != runnerpkg.PhaseSettled {
		return nil, fmt.Errorf("runner did not settle: %+v", view)
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

func configureExampleOrchestrator(ctx context.Context, root string, runtime *exampleRuntime, client *llm.Client) (*orchestrate.Orchestrator, error) {
	if client == nil {
		return nil, fmt.Errorf("example requires a non-nil LLM client")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	o := orchestrate.NewOrchestrator(ctx, logger)

	plannerSkill, err := orchestrate.NewEmbeddedOrchestratorSkill(client, nil, nil)
	if err != nil {
		return nil, err
	}
	if err := o.SetOrchestratorSkill(plannerSkill); err != nil {
		return nil, err
	}

	skillCfg := &llm.ConversationConfig{MaxSteps: 4}
	for _, spec := range []struct {
		dir   string
		tools []llm.Tool
	}{
		{dir: "elevation_lookup", tools: []llm.Tool{runtime.lookupElevationsTool()}},
		{dir: "train_gp_model", tools: []llm.Tool{runtime.trainGPModelTool()}},
		{dir: "markdown_to_pdf", tools: []llm.Tool{runtime.renderPDFTool()}},
	} {
		sk, err := llm.NewSkill(filepath.Join(root, spec.dir), nil, nil, spec.tools...)
		if err != nil {
			return nil, err
		}
		if err := o.AddSkills(orchestrate.AddSkillConfig{Skill: sk, Client: client, Config: skillCfg}); err != nil {
			return nil, err
		}
	}

	return o, nil
}

func buildGroundwaterRequest(measurements string) string {
	return "Use the following messy JSON to build a model that predicts groundwater water level at arbitrary Honshu locations. Save prediction values as CSV for later analysis. Do not make a PDF.\n\n```json\n" +
		strings.TrimSpace(measurements) +
		"\n```"
}

func streamRunnerUpdates(ctx context.Context, out io.Writer, updates <-chan runnerpkg.RunnerUpdate) error {
	encoder := json.NewEncoder(out)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			if err := encoder.Encode(update); err != nil {
				return err
			}
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

func (rt *exampleRuntime) lookupElevationsTool() llm.Tool {
	return llm.NewFuncTool("lookup_elevations", "Enrich groundwater observations with elevation values.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"observations_json": map[string]any{"type": "string"},
		},
		"required":             []string{"observations_json"},
		"additionalProperties": false,
	}, func(ctx context.Context, arguments string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return runSkillScript(ctx, filepath.Join(rt.root, "elevation_lookup"), "scripts/enrich.py", arguments)
	})
}

func (rt *exampleRuntime) trainGPModelTool() llm.Tool {
	return llm.NewFuncTool("train_gp_model", "Train a GP-style groundwater model and write CSV and plot artifacts.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"parent_exchange": map[string]any{"type": "string"},
		},
		"required":             []string{"parent_exchange"},
		"additionalProperties": false,
	}, func(ctx context.Context, arguments string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return rt.runSkillScript(ctx, filepath.Join(rt.root, "train_gp_model"), "scripts/train.py", arguments)
	})
}

func (rt *exampleRuntime) renderPDFTool() llm.Tool {
	return llm.NewFuncTool("render_markdown_pdf", "Render markdown as PDF when the user explicitly asks for PDF output.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"markdown": map[string]any{"type": "string"},
		},
		"required":             []string{"markdown"},
		"additionalProperties": false,
	}, func(ctx context.Context, arguments string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return rt.runSkillScript(ctx, filepath.Join(rt.root, "markdown_to_pdf"), "scripts/render.py", arguments)
	})
}

func runSkillScript(ctx context.Context, skillDir string, script string, inputJSON string) (string, error) {
	return (&exampleRuntime{}).runSkillScript(ctx, skillDir, script, inputJSON)
}

func (rt *exampleRuntime) runSkillScript(ctx context.Context, skillDir string, script string, inputJSON string) (string, error) {
	cmd := exec.CommandContext(ctx, "uv", "run", "--quiet", "python", script)
	cmd.Dir = skillDir
	cmd.Env = append(os.Environ(),
		"UV_CACHE_DIR="+filepath.Join(os.TempDir(), "dango-uv-cache"),
		"UV_PYTHON_DOWNLOADS=never",
	)
	if rt != nil && rt.artifactsDir != "" {
		cmd.Env = append(cmd.Env, "DANGO_ARTIFACTS_DIR="+rt.artifactsDir)
	}
	cmd.Stdin = strings.NewReader(inputJSON)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run %s in %s: %w\n%s", script, skillDir, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}
