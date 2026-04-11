package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/llm"
	promptassets "github.com/tsumina/dango/internal/prompts"
	"github.com/tsumina/dango/internal/spec"
	"gopkg.in/yaml.v3"
)

type llmClientFactory func(model string, logger *slog.Logger) llm.Client

func defaultLLMClientFactory(model string, logger *slog.Logger) llm.Client {
	return llm.NewOpenAICompatibleFromEnv(model, logger)
}

// ExecuteGenerationResult is the structured output of the built-in
// execute-generation AI call.
type ExecuteGenerationResult struct {
	Summary            string              `json:"summary,omitempty"`
	HandoffBody        string              `json:"handoff_body"`
	ExpectedOutputs    []string            `json:"expected_outputs,omitempty"`
	GeneratedArtifacts []GeneratedArtifact `json:"generated_artifacts"`
}

// GeneratedArtifact describes one AI-generated file produced during execution.
type GeneratedArtifact struct {
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content"`
	Private     bool   `json:"private,omitempty"`
}

// planWithBuiltInAI executes the built-in executor detail-planning path.
//
// It renders the detail-planning prompt, requests structured JSON from the
// configured model, and validates that the returned executor plan includes the
// minimum information required for downstream execution. The response ID from
// the underlying Responses API call is returned alongside the plan so callers
// can continue the conversation in a subsequent turn.
func (e *Executor) planWithBuiltInAI(ctx context.Context, runtimeContext runtimeContext, toolSpec spec.ToolSpec) (spec.ExecutorPlan, string, error) {
	prompt, err := e.renderDetailPlanPrompt(runtimeContext, toolSpec)
	if err != nil {
		return spec.ExecutorPlan{}, "", llm.NewCannotProceedError(
			llm.ModuleExecutor,
			llm.KindDetailPlanning,
			"failed to render built-in AI detail-planning prompt",
			err,
		)
	}

	payload, responseID, err := e.completeJSON(ctx, toolSpec.Model, prompt, "Refine the executor stage now and return JSON only.", "")
	if err != nil {
		return spec.ExecutorPlan{}, "", llm.NewCannotProceedError(
			llm.ModuleExecutor,
			llm.KindDetailPlanning,
			"built-in AI detail planning failed",
			err,
		)
	}

	var plan spec.ExecutorPlan
	if err := json.Unmarshal(payload, &plan); err != nil {
		return spec.ExecutorPlan{}, "", llm.NewCannotProceedError(
			llm.ModuleExecutor,
			llm.KindDetailPlanning,
			"built-in AI detail planning returned invalid JSON",
			err,
		)
	}

	plan.Summary = strings.TrimSpace(plan.Summary)
	plan.SubTask = strings.TrimSpace(plan.SubTask)
	plan.ExpectedOutputs = cleanOutputPaths(plan.ExpectedOutputs)
	if plan.Summary == "" || plan.SubTask == "" || len(plan.ExpectedOutputs) == 0 {
		return spec.ExecutorPlan{}, "", llm.NewCannotProceedError(
			llm.ModuleExecutor,
			llm.KindDetailPlanning,
			"built-in AI detail planning did not produce a complete executor plan",
			nil,
		)
	}

	return plan, responseID, nil
}

// runWithBuiltInAI executes the built-in executor execute-generation path
// using a two-turn conversation via the OpenAI Responses API.
//
// Turn one sends the detail-planning prompt and validates the resulting executor
// plan. The response ID from that turn is then passed as PreviousResponseID in
// turn two, so the model carries forward the planning context when generating
// the concrete stage artifacts. This two-turn design avoids duplicating the full
// planning context in the generation request while keeping the model grounded in
// the decisions made during planning.
func (e *Executor) runWithBuiltInAI(ctx context.Context, runtimeContext runtimeContext, toolSpec spec.ToolSpec) error {
	// Turn 1: planning — validate the executor stage before generation.
	_, planResponseID, err := e.planWithBuiltInAI(ctx, runtimeContext, toolSpec)
	if err != nil {
		return err
	}

	// Turn 2: generation — continue the conversation so the model retains the
	// planning context and produces concrete artifacts.
	prompt, err := e.renderExecutePrompt(runtimeContext, toolSpec)
	if err != nil {
		return llm.NewCannotProceedError(
			llm.ModuleExecutor,
			llm.KindExecuteGeneration,
			"failed to render built-in AI execute-generation prompt",
			err,
		)
	}

	payload, _, err := e.completeJSON(ctx, toolSpec.Model, prompt, "Generate the stage outputs now and return JSON only.", planResponseID)
	if err != nil {
		return llm.NewCannotProceedError(
			llm.ModuleExecutor,
			llm.KindExecuteGeneration,
			"built-in AI execute generation failed",
			err,
		)
	}

	var result ExecuteGenerationResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return llm.NewCannotProceedError(
			llm.ModuleExecutor,
			llm.KindExecuteGeneration,
			"built-in AI execute generation returned invalid JSON",
			err,
		)
	}

	normalized, publicFiles, err := normalizeExecuteGenerationResult(result)
	if err != nil {
		return llm.NewCannotProceedError(
			llm.ModuleExecutor,
			llm.KindExecuteGeneration,
			"built-in AI execute generation returned an invalid result",
			err,
		)
	}

	if err := writeGeneratedArtifacts(runtimeContext.PublicOutputPath, runtimeContext.PrivateOutputPath, normalized.GeneratedArtifacts); err != nil {
		return fmt.Errorf("write AI-generated artifacts: %w", err)
	}

	handoff := spec.Handoff{
		Metadata: spec.HandoffMetadata{
			TaskID:      runtimeContext.TaskID,
			Tool:        toolSpec.Name,
			Status:      spec.HandoffStatusCompleted,
			OutputFiles: publicFiles,
			Timestamp:   time.Now().UTC(),
		},
		Body: normalized.HandoffBody,
	}
	if err := writePublicHandoff(runtimeContext.PublicOutputPath, handoff); err != nil {
		return err
	}
	if err := writePrivateHandoff(runtimeContext.PrivateOutputPath, handoff); err != nil {
		return err
	}
	return nil
}

// renderDetailPlanPrompt builds the detail-planning prompt from the current
// executor runtime context.
func (e *Executor) renderDetailPlanPrompt(runtimeContext runtimeContext, toolSpec spec.ToolSpec) (string, error) {
	toolJSON, toolConfigYAML, inputContextJSON, err := e.promptContext(runtimeContext, toolSpec)
	if err != nil {
		return "", err
	}
	return promptassets.RenderExecutorDetailPlan(promptassets.ExecutorDetailPlanInput{
		TaskID:           runtimeContext.TaskID,
		SubTask:          mustReadTrimmed(runtimeContext.SubTaskPath),
		ToolJSON:         toolJSON,
		ToolConfigYAML:   toolConfigYAML,
		InputContextJSON: inputContextJSON,
	})
}

// renderExecutePrompt builds the execute-generation prompt from the current
// executor runtime context and default output hints.
func (e *Executor) renderExecutePrompt(runtimeContext runtimeContext, toolSpec spec.ToolSpec) (string, error) {
	toolJSON, toolConfigYAML, inputContextJSON, err := e.promptContext(runtimeContext, toolSpec)
	if err != nil {
		return "", err
	}
	expectedOutputsPayload, err := json.MarshalIndent(defaultOutputHints(toolSpec), "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal executor output hints: %w", err)
	}
	return promptassets.RenderExecutorExecute(promptassets.ExecutorExecuteInput{
		TaskID:              runtimeContext.TaskID,
		SubTask:             mustReadTrimmed(runtimeContext.SubTaskPath),
		ToolJSON:            toolJSON,
		ToolConfigYAML:      toolConfigYAML,
		InputContextJSON:    inputContextJSON,
		ExpectedOutputsJSON: string(expectedOutputsPayload),
	})
}

// promptContext collects the serialized tool, config, and input context shared
// by the built-in detail-planning and execute-generation prompts.
func (e *Executor) promptContext(runtimeContext runtimeContext, toolSpec spec.ToolSpec) (string, string, string, error) {
	toolPayload, err := json.MarshalIndent(toolSpec, "", "  ")
	if err != nil {
		return "", "", "", fmt.Errorf("marshal tool spec for prompt: %w", err)
	}
	toolConfigYAML, err := loadToolConfigPromptYAML(runtimeContext.ToolConfigPath, toolSpec)
	if err != nil {
		return "", "", "", err
	}
	inputContextJSON, err := summarizeInputContext(runtimeContext)
	if err != nil {
		return "", "", "", err
	}
	return string(toolPayload), toolConfigYAML, inputContextJSON, nil
}

// completeJSON resolves a model-scoped LLM client and requests structured JSON
// for the provided prompts. When previousResponseID is non-empty the underlying
// Responses API continues the identified conversation turn.
func (e *Executor) completeJSON(ctx context.Context, model string, systemPrompt string, userPrompt string, previousResponseID string) ([]byte, string, error) {
	if e.llmFactory == nil {
		return nil, "", fmt.Errorf("built-in executor AI client factory is not configured")
	}
	client := e.llmFactory(strings.TrimSpace(model), e.logger)
	if client == nil {
		return nil, "", fmt.Errorf("built-in executor AI client is not configured")
	}
	return client.CompleteJSON(ctx, llm.Request{
		SystemPrompt:       systemPrompt,
		UserPrompt:         userPrompt,
		Temperature:        0.1,
		PreviousResponseID: previousResponseID,
	})
}

// loadToolConfigPromptYAML returns the merged tool config payload used in
// executor prompts, falling back to the local tool spec when the merged config
// file is unavailable.
func loadToolConfigPromptYAML(path string, toolSpec spec.ToolSpec) (string, error) {
	if strings.TrimSpace(path) != "" {
		payload, err := os.ReadFile(path)
		if err == nil {
			return strings.TrimSpace(string(payload)), nil
		}
	}
	payload, err := yaml.Marshal(toolSpec)
	if err != nil {
		return "", fmt.Errorf("marshal fallback tool config yaml: %w", err)
	}
	return strings.TrimSpace(string(payload)), nil
}

// mustReadTrimmed reads a small text artifact for prompt rendering and returns
// an empty string when the file is unavailable.
func mustReadTrimmed(path string) string {
	payload, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(payload))
}

// defaultOutputHints derives the fallback public artifact names suggested to
// the built-in execute-generation prompt from the tool's declared output types.
func defaultOutputHints(toolSpec spec.ToolSpec) []string {
	out := make([]string, 0, len(toolSpec.OutputTypes))
	for _, outputType := range toolSpec.OutputTypes {
		outputType = strings.TrimSpace(outputType)
		if outputType == "" {
			continue
		}
		out = append(out, "result."+outputType)
	}
	return out
}

// normalizeExecuteGenerationResult validates and normalizes the model-produced
// execute-generation result before any files are written.
//
// The function ensures a handoff body exists, all generated artifact paths are
// safe and non-empty, at least one public artifact is present, and the declared
// expected outputs line up with the generated public artifacts.
func normalizeExecuteGenerationResult(result ExecuteGenerationResult) (ExecuteGenerationResult, []string, error) {
	result.Summary = strings.TrimSpace(result.Summary)
	result.HandoffBody = strings.TrimSpace(result.HandoffBody)
	if result.HandoffBody == "" && result.Summary != "" {
		result.HandoffBody = "## Description\n\n" + result.Summary
	}
	if result.HandoffBody == "" {
		return ExecuteGenerationResult{}, nil, fmt.Errorf("handoff_body is required")
	}

	publicFiles := make([]string, 0, len(result.GeneratedArtifacts))
	normalizedArtifacts := make([]GeneratedArtifact, 0, len(result.GeneratedArtifacts))
	publicSet := map[string]bool{}
	for _, artifact := range result.GeneratedArtifacts {
		cleanPath, err := normalizeGeneratedPath(artifact.Path)
		if err != nil {
			return ExecuteGenerationResult{}, nil, err
		}
		if strings.TrimSpace(artifact.Content) == "" {
			return ExecuteGenerationResult{}, nil, fmt.Errorf("generated artifact %q must include content", cleanPath)
		}
		artifact.Path = cleanPath
		artifact.Description = strings.TrimSpace(artifact.Description)
		normalizedArtifacts = append(normalizedArtifacts, artifact)
		if !artifact.Private && !publicSet[cleanPath] {
			publicSet[cleanPath] = true
			publicFiles = append(publicFiles, cleanPath)
		}
	}
	if len(normalizedArtifacts) == 0 {
		return ExecuteGenerationResult{}, nil, fmt.Errorf("generated_artifacts must contain at least one artifact")
	}
	if len(publicFiles) == 0 {
		return ExecuteGenerationResult{}, nil, fmt.Errorf("generated_artifacts must include at least one public artifact")
	}
	sort.Strings(publicFiles)

	result.GeneratedArtifacts = normalizedArtifacts
	result.ExpectedOutputs = cleanOutputPaths(result.ExpectedOutputs)
	if len(result.ExpectedOutputs) == 0 {
		result.ExpectedOutputs = append([]string(nil), publicFiles...)
	}
	for _, expected := range result.ExpectedOutputs {
		if !publicSet[expected] {
			return ExecuteGenerationResult{}, nil, fmt.Errorf("expected output %q does not match any public generated artifact", expected)
		}
	}

	return result, publicFiles, nil
}

// cleanOutputPaths normalizes a set of relative output paths and drops invalid
// or duplicate entries.
func cleanOutputPaths(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		cleanPath, err := normalizeGeneratedPath(value)
		if err != nil || seen[cleanPath] {
			continue
		}
		seen[cleanPath] = true
		out = append(out, cleanPath)
	}
	return out
}

// normalizeGeneratedPath validates that a generated artifact path stays within
// the output root and does not collide with reserved handoff filenames.
func normalizeGeneratedPath(value string) (string, error) {
	value = filepath.ToSlash(filepath.Clean(strings.TrimSpace(value)))
	if value == "" || value == "." {
		return "", fmt.Errorf("artifact path is required")
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "../") || strings.Contains(value, "/../") {
		return "", fmt.Errorf("artifact path %q must be relative and stay within the output root", value)
	}
	if value == "handoff.md" || value == "_handoff.md" {
		return "", fmt.Errorf("artifact path %q is reserved", value)
	}
	return value, nil
}

// writeGeneratedArtifacts materializes generated artifacts into the private
// output tree and mirrors public artifacts into the public output tree.
func writeGeneratedArtifacts(publicOutputPath string, privateOutputPath string, artifacts []GeneratedArtifact) error {
	for _, artifact := range artifacts {
		privatePath := filepath.Join(privateOutputPath, filepath.FromSlash(artifact.Path))
		if err := os.MkdirAll(filepath.Dir(privatePath), 0o755); err != nil {
			return fmt.Errorf("create private artifact directory for %q: %w", artifact.Path, err)
		}
		if err := os.WriteFile(privatePath, []byte(artifact.Content), 0o644); err != nil {
			return fmt.Errorf("write private artifact %q: %w", artifact.Path, err)
		}
		if artifact.Private {
			continue
		}

		publicPath := filepath.Join(publicOutputPath, filepath.FromSlash(artifact.Path))
		if err := os.MkdirAll(filepath.Dir(publicPath), 0o755); err != nil {
			return fmt.Errorf("create public artifact directory for %q: %w", artifact.Path, err)
		}
		if err := os.WriteFile(publicPath, []byte(artifact.Content), 0o644); err != nil {
			return fmt.Errorf("write public artifact %q: %w", artifact.Path, err)
		}
	}
	return nil
}
