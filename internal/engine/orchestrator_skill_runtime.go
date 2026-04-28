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

func planWithOrchestrator(ctx context.Context, req *Request, skills []runnerpkg.SkillSummary, runtimeSkill *llm.Skill) (*CoarsePlan, *RejectReason, error) {
	prompt, err := marshalOrchestratorPlanningInput(req.Input, skills)
	if err != nil {
		return nil, nil, fmt.Errorf("orchestrate: marshal planner input: %w", err)
	}
	raw, err := runtimeSkill.Run(normalizeContext(ctx), prompt, defaultPlanningEffort)
	if err != nil {
		return nil, nil, err
	}
	plan, reject, err := parseOrchestratorPlanningOutput(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("orchestrate: %w", err)
	}
	return plan, reject, nil
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
