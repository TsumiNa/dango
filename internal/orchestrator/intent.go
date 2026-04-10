package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tsumina/dango/internal/aihook"
	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/logging"
	promptassets "github.com/tsumina/dango/internal/prompts"
	"github.com/tsumina/dango/internal/taskflow"
)

type builtInIntentUnderstandingHook struct {
	llm    llm.Client
	logger *slog.Logger
}

// NewIntentUnderstandingHook constructs the built-in orchestrator intent-understanding hook.
func NewIntentUnderstandingHook(model string, logger *slog.Logger) aihook.IntentUnderstandingHook {
	return NewIntentUnderstandingHookWithClient(llm.NewOpenAICompatibleFromEnv(model, logger), logger)
}

// NewIntentUnderstandingHookWithClient constructs the built-in intent hook with an explicit LLM client.
func NewIntentUnderstandingHookWithClient(client llm.Client, logger *slog.Logger) aihook.IntentUnderstandingHook {
	return &builtInIntentUnderstandingHook{
		llm:    client,
		logger: logging.Component(logger, "orchestrator.intent_hook"),
	}
}

func (h *builtInIntentUnderstandingHook) Understand(ctx context.Context, request aihook.IntentRequest) (aihook.IntentResult, error) {
	if h.llm == nil {
		return aihook.IntentResult{}, aihook.NewCannotProceedError(
			aihook.ModuleOrchestrator,
			aihook.KindIntentUnderstanding,
			"built-in intent-understanding LLM is not configured",
			nil,
		)
	}

	prompt, err := promptassets.RenderIntentUnderstand(request.Request, request.Entry)
	if err != nil {
		return aihook.IntentResult{}, aihook.NewCannotProceedError(
			aihook.ModuleOrchestrator,
			aihook.KindIntentUnderstanding,
			"failed to render intent-understanding prompt",
			err,
		)
	}

	payload, err := h.llm.CompleteJSON(ctx, llm.Request{
		SystemPrompt: prompt,
		UserPrompt:   "Normalize the request now and return JSON only.",
		Temperature:  0.1,
	})
	if err != nil {
		return aihook.IntentResult{}, aihook.NewCannotProceedError(
			aihook.ModuleOrchestrator,
			aihook.KindIntentUnderstanding,
			"built-in intent-understanding LLM failed",
			err,
		)
	}

	var result aihook.IntentResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return aihook.IntentResult{}, aihook.NewCannotProceedError(
			aihook.ModuleOrchestrator,
			aihook.KindIntentUnderstanding,
			"built-in intent-understanding LLM returned invalid JSON",
			err,
		)
	}

	normalized := taskflow.NormalizeRequestEnvelope(result.Request)
	if taskflow.PrimaryRequestText(normalized) == "" && len(normalized.Parts) == 0 {
		return aihook.IntentResult{}, aihook.NewCannotProceedError(
			aihook.ModuleOrchestrator,
			aihook.KindIntentUnderstanding,
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

func applyIntentResult(original taskflow.RequestEnvelope, result aihook.IntentResult) taskflow.RequestEnvelope {
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

func understandRequest(ctx context.Context, hook aihook.IntentUnderstandingHook, request taskflow.RequestEnvelope) (taskflow.RequestEnvelope, error) {
	if hook == nil {
		return taskflow.RequestEnvelope{}, aihook.NewCannotProceedError(
			aihook.ModuleOrchestrator,
			aihook.KindIntentUnderstanding,
			"no intent-understanding hook is configured for the orchestrator entrypoint",
			nil,
		)
	}

	result, err := hook.Understand(ctx, aihook.IntentRequest{
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
