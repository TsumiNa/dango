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

type builtInIntentUnderstandingHook struct {
	llm    llm.Client
	logger *slog.Logger
}

// NewIntentUnderstandingHook constructs the built-in orchestrator
// intent-understanding hook with an explicit LLM client.
//
// Callers are responsible for choosing how that client is configured. Production
// wiring typically passes [llm.NewOpenAICompatibleFromEnv], while tests and
// alternate transport wiring can inject a stub or custom client directly.
func NewIntentUnderstandingHook(client llm.Client, logger *slog.Logger) llm.IntentUnderstandingHook {
	return &builtInIntentUnderstandingHook{
		llm:    client,
		logger: logging.Component(logger, "orchestrator.intent_hook"),
	}
}

// Understand implements llm.IntentUnderstandingHook using the repository's
// built-in prompt assets and JSON completion path.
//
// The method renders the intent-understanding prompt, asks the LLM for a
// normalized request envelope plus structured metadata, trims the returned
// summary fields, and validates that the result can still drive the downstream
// task workflow without losing ingress context.
func (h *builtInIntentUnderstandingHook) Understand(ctx context.Context, request llm.IntentRequest) (llm.IntentResult, error) {
	if h.llm == nil {
		return llm.IntentResult{}, llm.NewCannotProceedError(
			llm.ModuleOrchestrator,
			llm.KindIntentUnderstanding,
			"built-in intent-understanding LLM is not configured",
			nil,
		)
	}

	prompt, err := promptassets.RenderIntentUnderstand(request.Request, request.Entry)
	if err != nil {
		return llm.IntentResult{}, llm.NewCannotProceedError(
			llm.ModuleOrchestrator,
			llm.KindIntentUnderstanding,
			"failed to render intent-understanding prompt",
			err,
		)
	}

	payload, _, err := h.llm.CompleteJSON(ctx, llm.Request{
		SystemPrompt: prompt,
		UserPrompt:   "Normalize the request now and return JSON only.",
		Temperature:  0.1,
	})
	if err != nil {
		return llm.IntentResult{}, llm.NewCannotProceedError(
			llm.ModuleOrchestrator,
			llm.KindIntentUnderstanding,
			"built-in intent-understanding LLM failed",
			err,
		)
	}

	var result llm.IntentResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return llm.IntentResult{}, llm.NewCannotProceedError(
			llm.ModuleOrchestrator,
			llm.KindIntentUnderstanding,
			"built-in intent-understanding LLM returned invalid JSON",
			err,
		)
	}

	normalized := taskflow.NormalizeRequestEnvelope(result.Request)
	if taskflow.PrimaryRequestText(normalized) == "" && len(normalized.Parts) == 0 {
		return llm.IntentResult{}, llm.NewCannotProceedError(
			llm.ModuleOrchestrator,
			llm.KindIntentUnderstanding,
			"built-in intent-understanding did not produce a usable request envelope",
			nil,
		)
	}
	result.Request = normalized
	result.Summary = strings.TrimSpace(result.Summary)
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	return result, nil
}

// applyIntentResult merges the intent-understanding result back onto the
// original request envelope.
//
// Original metadata is preserved unless the hook explicitly overwrites a key,
// and any hook-provided summary is stored under the intent_summary metadata
// field for later inspection.
func applyIntentResult(original taskflow.RequestEnvelope, result llm.IntentResult) taskflow.RequestEnvelope {
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

// understandRequest runs the configured intent-understanding hook and returns a
// normalized request envelope suitable for task creation.
//
// The helper injects request-entry metadata from context, enforces that a hook
// is present, and rejects hook results that collapse into an empty request.
func understandRequest(ctx context.Context, hook llm.IntentUnderstandingHook, request taskflow.RequestEnvelope) (taskflow.RequestEnvelope, error) {
	if hook == nil {
		return taskflow.RequestEnvelope{}, llm.NewCannotProceedError(
			llm.ModuleOrchestrator,
			llm.KindIntentUnderstanding,
			"no intent-understanding hook is configured for the orchestrator entrypoint",
			nil,
		)
	}

	result, err := hook.Understand(ctx, llm.IntentRequest{
		Request: request,
		Entry:   taskflow.RequestMetadataFromContext(ctx),
	})
	if err != nil {
		return taskflow.RequestEnvelope{}, err
	}

	normalized := applyIntentResult(request, result)
	if taskflow.PrimaryRequestText(normalized) == "" && len(normalized.Parts) == 0 {
		return taskflow.RequestEnvelope{}, fmt.Errorf("intent understanding returned an empty request envelope")
	}
	return normalized, nil
}
