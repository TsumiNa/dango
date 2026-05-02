package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
)

const (
	defaultPlanningEffort llm.ReasoningEffort = llm.ReasoningEffortMedium
)

var errOrchestratorSkillUnconfigured = errors.New("orchestrate: orchestrator skill has no runnable llm client")

// OrchestratorProgressFunc receives streaming progress from orchestrator-owned
// LLM work before a runner exists.
type OrchestratorProgressFunc func(OrchestratorProgressEvent)

// OrchestratorProgressEvent is a compact stream item from the orchestrator
// skill. It intentionally does not carry runner snapshots; runner state remains
// runner-owned and is streamed through runner update APIs after planning.
type OrchestratorProgressEvent struct {
	Type    string `json:"type"`
	Delta   string `json:"delta,omitempty"`
	Message string `json:"message,omitempty"`
}

const (
	OrchestratorProgressStatus    = "status"
	OrchestratorProgressReasoning = "reasoning_delta"
	OrchestratorProgressText      = "text_delta"
)

func planWithOrchestrator(ctx context.Context, req *Request, skills []runnerpkg.SkillSummary, runtimeSkill *llm.Skill, eventStream *streampkg.Stream, progress OrchestratorProgressFunc) (*CoarsePlan, *RejectReason, error) {
	prompt, err := marshalOrchestratorPlanningInput(req.Input, skills)
	if err != nil {
		return nil, nil, fmt.Errorf("orchestrate: marshal planner input: %w", err)
	}
	if progress != nil {
		return planWithOrchestratorStream(ctx, prompt, runtimeSkill, eventStream, progress)
	}
	return planWithOrchestratorRun(ctx, prompt, runtimeSkill, eventStream)
}

func planWithOrchestratorRun(ctx context.Context, prompt string, runtimeSkill *llm.Skill, eventStream *streampkg.Stream) (*CoarsePlan, *RejectReason, error) {
	ctx = normalizeContext(ctx)
	raw, err := runtimeSkill.Run(ctx, prompt, defaultPlanningEffort)
	if err != nil {
		return nil, nil, err
	}
	if raw != "" {
		emitEngineStreamEvent(ctx, eventStream,
			streamSourceOrchestrator(),
			streampkg.EventLLMOutputDelta,
			streampkg.StatusCompleted,
			raw,
			streampkg.Scope{},
			map[string]any{"stage": "planning"},
		)
	}
	emitEngineStreamEvent(ctx, eventStream,
		streamSourceOrchestrator(),
		streampkg.EventStatusCompleted,
		streampkg.StatusCompleted,
		"orchestrator planning completed",
		streampkg.Scope{},
		map[string]any{"stage": "planning"},
	)
	return parsePlanningResult(raw)
}

func planWithOrchestratorStream(ctx context.Context, prompt string, runtimeSkill *llm.Skill, eventStream *streampkg.Stream, progress OrchestratorProgressFunc) (*CoarsePlan, *RejectReason, error) {
	ctx = normalizeContext(ctx)
	conv := runtimeSkill.Conversation()
	if conv == nil {
		return nil, nil, fmt.Errorf("orchestrate: orchestrator skill is not bound")
	}
	emitPlanningProgress(ctx, eventStream, progress, OrchestratorProgressEvent{Type: OrchestratorProgressStatus, Message: "orchestrator planning stream started"})
	conv.AppendUser(prompt)
	stream, err := conv.Stream(ctx, defaultPlanningEffort)
	if err != nil {
		return nil, nil, err
	}
	var raw string
	for event := range stream {
		if event.Err != nil {
			emitEngineStreamEvent(ctx, eventStream,
				streamSourceOrchestrator(),
				streampkg.EventStatusFailed,
				streampkg.StatusFailed,
				event.Err.Error(),
				streampkg.Scope{},
				map[string]any{"stage": "planning"},
			)
			return nil, nil, event.Err
		}
		if event.ReasoningDelta != "" {
			emitPlanningProgress(ctx, eventStream, progress, OrchestratorProgressEvent{Type: OrchestratorProgressReasoning, Delta: event.ReasoningDelta})
		}
		if event.TextDelta != "" {
			raw += event.TextDelta
			emitPlanningProgress(ctx, eventStream, progress, OrchestratorProgressEvent{Type: OrchestratorProgressText, Delta: event.TextDelta})
		}
	}
	if raw == "" {
		raw = lastAssistantText(conv)
	}
	emitPlanningProgress(ctx, eventStream, progress, OrchestratorProgressEvent{Type: OrchestratorProgressStatus, Message: "orchestrator planning stream completed"})
	return parsePlanningResult(raw)
}

func emitPlanningProgress(ctx context.Context, eventStream *streampkg.Stream, progress OrchestratorProgressFunc, event OrchestratorProgressEvent) {
	if progress != nil {
		progress(event)
	}
	switch event.Type {
	case OrchestratorProgressStatus:
		emitEngineStreamEvent(ctx, eventStream,
			streamSourceOrchestrator(),
			streampkg.EventStatusProgress,
			streampkg.StatusRunning,
			event.Message,
			streampkg.Scope{},
			map[string]any{"stage": "planning"},
		)
	case OrchestratorProgressReasoning:
		emitEngineStreamEvent(ctx, eventStream,
			streamSourceOrchestrator(),
			streampkg.EventLLMReasoningDelta,
			streampkg.StatusRunning,
			event.Delta,
			streampkg.Scope{},
			map[string]any{"stage": "planning"},
		)
	case OrchestratorProgressText:
		emitEngineStreamEvent(ctx, eventStream,
			streamSourceOrchestrator(),
			streampkg.EventLLMOutputDelta,
			streampkg.StatusRunning,
			event.Delta,
			streampkg.Scope{},
			map[string]any{"stage": "planning"},
		)
	}
}

func parsePlanningResult(raw string) (*CoarsePlan, *RejectReason, error) {
	plan, reject, err := parseOrchestratorPlanningOutput(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("orchestrate: %w", err)
	}
	return plan, reject, nil
}

func lastAssistantText(conv *llm.Conversation) string {
	if conv == nil {
		return ""
	}
	turns := conv.Turns()
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == llm.RoleAssistant && turns[i].Text != "" {
			return turns[i].Text
		}
	}
	return ""
}

func runtimeOrchestrator(sk *llm.Skill, envClient *llm.Client, envClientErr error) (*llm.Skill, error) {
	if sk == nil {
		return nil, errOrchestratorSkillUnconfigured
	}
	if sk.Client() != nil {
		return bindOrchestratorSkill(sk, sk.Client())
	}
	if envClientErr == nil && envClient != nil {
		return bindOrchestratorSkill(sk, envClient)
	}
	return nil, errOrchestratorSkillUnconfigured
}

func bindOrchestratorSkill(sk *llm.Skill, client *llm.Client) (*llm.Skill, error) {
	if sk.Client() == client && sk.Conversation() != nil {
		return sk, nil
	}
	return sk.Bind(client, nil, nil)
}

func collectSkillSummaries(skills map[string]*llm.Skill) []runnerpkg.SkillSummary {
	out := make([]runnerpkg.SkillSummary, 0, len(skills))
	for name, sk := range skills {
		summary := runnerpkg.SkillSummary{Name: name}
		if sk != nil {
			summary.Description = sk.Description
		}
		out = append(out, summary)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
