package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/logging"
	promptassets "github.com/tsumina/dango/internal/prompts"
	"github.com/tsumina/dango/internal/taskflow"
)

// intentRequest is the input to the orchestrator intent-understanding call.
type intentRequest struct {
	Request taskflow.RequestEnvelope `json:"request"`
	Entry   taskflow.RequestMetadata `json:"entry"`
}

// intentResult is the structured output of intent understanding.
type intentResult struct {
	Request  taskflow.RequestEnvelope `json:"request"`
	Metadata map[string]string        `json:"metadata,omitempty"`
	Summary  string                   `json:"summary,omitempty"`
}

// understandIntent calls the orchestrator's LLM client to normalize the
// inbound request envelope.
//
// When client is nil, the function returns the original request unchanged so
// the orchestrator can run without a configured AI backend. When the LLM is
// available, the result is merged back onto the original envelope so ingress
// metadata is always preserved.
func understandIntent(ctx context.Context, client llm.Client, logger *slog.Logger, request taskflow.RequestEnvelope) (taskflow.RequestEnvelope, error) {
	if client == nil {
		return request, nil
	}

	entry := taskflow.RequestMetadataFromContext(ctx)
	prompt, err := promptassets.RenderIntentUnderstand(request, entry)
	if err != nil {
		return taskflow.RequestEnvelope{}, llm.NewCannotProceedError(
			llm.ModuleOrchestrator,
			llm.KindIntentUnderstanding,
			"failed to render intent-understanding prompt",
			err,
		)
	}

	payload, _, err := client.CompleteJSON(ctx, llm.Request{
		SystemPrompt: prompt,
		UserPrompt:   "Normalize the request now and return JSON only.",
		Temperature:  0.1,
	})
	if err != nil {
		return taskflow.RequestEnvelope{}, llm.NewCannotProceedError(
			llm.ModuleOrchestrator,
			llm.KindIntentUnderstanding,
			"intent-understanding LLM call failed",
			err,
		)
	}

	var result intentResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return taskflow.RequestEnvelope{}, llm.NewCannotProceedError(
			llm.ModuleOrchestrator,
			llm.KindIntentUnderstanding,
			"intent-understanding LLM returned invalid JSON",
			err,
		)
	}

	normalized := taskflow.NormalizeRequestEnvelope(result.Request)
	if taskflow.PrimaryRequestText(normalized) == "" && len(normalized.Parts) == 0 {
		return taskflow.RequestEnvelope{}, llm.NewCannotProceedError(
			llm.ModuleOrchestrator,
			llm.KindIntentUnderstanding,
			"intent understanding did not produce a usable request envelope",
			nil,
		)
	}

	merged := applyIntentResult(request, result)
	if taskflow.PrimaryRequestText(merged) == "" && len(merged.Parts) == 0 {
		return taskflow.RequestEnvelope{}, fmt.Errorf("intent understanding returned an empty request envelope")
	}

	if logger != nil {
		logging.Component(logger, "orchestrator.intent").Debug("intent understanding completed", "summary", strings.TrimSpace(result.Summary))
	}

	return merged, nil
}

// applyIntentResult merges the intent-understanding result back onto the
// original request envelope.
func applyIntentResult(original taskflow.RequestEnvelope, result intentResult) taskflow.RequestEnvelope {
	merged := taskflow.NormalizeRequestEnvelope(result.Request)
	if merged.Meta == nil {
		merged.Meta = map[string]string{}
	}
	for key, value := range original.Meta {
		if _, exists := merged.Meta[key]; !exists {
			merged.Meta[key] = value
		}
	}
	for key, value := range result.Metadata {
		merged.Meta[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if strings.TrimSpace(result.Summary) != "" {
		merged.Meta["intent_summary"] = strings.TrimSpace(result.Summary)
	}
	return merged
}
