package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
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

func planWithOrchestrator(ctx context.Context, req *Request, skills []runnerpkg.SkillSummary, runtimeSkill *llm.Skill, progress OrchestratorProgressFunc) (*CoarsePlan, *RejectReason, error) {
	prompt, err := marshalOrchestratorPlanningInput(req.Input, skills)
	if err != nil {
		return nil, nil, fmt.Errorf("orchestrate: marshal planner input: %w", err)
	}
	if progress != nil {
		return planWithOrchestratorStream(ctx, prompt, runtimeSkill, progress)
	}
	raw, err := runtimeSkill.Run(normalizeContext(ctx), prompt, defaultPlanningEffort)
	if err != nil {
		return nil, nil, err
	}
	return parsePlanningResult(raw)
}

func planWithOrchestratorStream(ctx context.Context, prompt string, runtimeSkill *llm.Skill, progress OrchestratorProgressFunc) (*CoarsePlan, *RejectReason, error) {
	ctx = normalizeContext(ctx)
	conv := runtimeSkill.Conversation()
	if conv == nil {
		return nil, nil, fmt.Errorf("orchestrate: orchestrator skill is not bound")
	}
	progress(OrchestratorProgressEvent{Type: OrchestratorProgressStatus, Message: "orchestrator planning stream started"})
	conv.AppendUser(prompt)
	stream, err := conv.Stream(ctx, defaultPlanningEffort)
	if err != nil {
		return nil, nil, err
	}
	var raw string
	for event := range stream {
		if event.Err != nil {
			return nil, nil, event.Err
		}
		if event.ReasoningDelta != "" {
			progress(OrchestratorProgressEvent{Type: OrchestratorProgressReasoning, Delta: event.ReasoningDelta})
		}
		if event.TextDelta != "" {
			raw += event.TextDelta
			progress(OrchestratorProgressEvent{Type: OrchestratorProgressText, Delta: event.TextDelta})
		}
	}
	if raw == "" {
		raw = lastAssistantText(conv)
	}
	progress(OrchestratorProgressEvent{Type: OrchestratorProgressStatus, Message: "orchestrator planning stream completed"})
	return parsePlanningResult(raw)
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
