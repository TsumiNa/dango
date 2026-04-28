package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/tsumina/dango/internal/llm"
)

const (
	defaultPlanningEffort llm.ReasoningEffort = llm.ReasoningEffortMedium
	defaultReviewEffort   llm.ReasoningEffort = llm.ReasoningEffortMedium
	defaultReplanEffort   llm.ReasoningEffort = llm.ReasoningEffortMedium
)

var errOrchestratorSkillUnconfigured = errors.New("orchestrate: orchestrator skill has no runnable llm client")

func planWithOrchestratorSkill(ctx context.Context, req *Request, skills map[string]*llm.Skill, orchestratorSkill *llm.Skill, envClient *llm.Client, envClientErr error) (*CoarsePlan, *RejectReason, error) {
	runtimeSkill, err := runtimeOrchestratorSkill(orchestratorSkill, envClient, envClientErr)
	if err != nil {
		if errors.Is(err, errOrchestratorSkillUnconfigured) {
			return rejectUnconfiguredPlan(req, skills, orchestratorSkill)
		}
		return nil, nil, err
	}
	prompt, err := marshalOrchestratorPlanningInput(req.Input, collectSkillSummaries(skills))
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

func reviewWithOrchestratorSkill(ctx context.Context, plan *CoarsePlan, polishFragments map[string]any, orchestratorSkill *llm.Skill, envClient *llm.Client, envClientErr error) (*PlanReview, error) {
	runtimeSkill, err := runtimeOrchestratorSkill(orchestratorSkill, envClient, envClientErr)
	if err != nil {
		return nil, err
	}
	prompt, err := marshalOrchestratorReviewInput(plan, polishFragments)
	if err != nil {
		return nil, fmt.Errorf("orchestrate: marshal review input: %w", err)
	}
	raw, err := runtimeSkill.Run(normalizeContext(ctx), prompt, defaultReviewEffort)
	if err != nil {
		return nil, err
	}
	decision, err := parseOrchestratorReviewOutput(raw)
	if err != nil {
		return nil, fmt.Errorf("orchestrate: %w", err)
	}
	return decision, nil
}

func replanWithOrchestratorSkill(ctx context.Context, request string, currentPlan *CoarsePlan, reason string, polishFragments map[string]any, skills map[string]*llm.Skill, orchestratorSkill *llm.Skill, envClient *llm.Client, envClientErr error) (*CoarsePlan, error) {
	runtimeSkill, err := runtimeOrchestratorSkill(orchestratorSkill, envClient, envClientErr)
	if err != nil {
		return nil, err
	}
	prompt, err := marshalOrchestratorReplanInput(request, currentPlan, reason, polishFragments, collectSkillSummaries(skills))
	if err != nil {
		return nil, fmt.Errorf("orchestrate: marshal replan input: %w", err)
	}
	raw, err := runtimeSkill.Run(normalizeContext(ctx), prompt, defaultReplanEffort)
	if err != nil {
		return nil, err
	}
	plan, err := parseOrchestratorReplanOutput(raw)
	if err != nil {
		return nil, fmt.Errorf("orchestrate: %w", err)
	}
	return plan, nil
}

func runtimeOrchestratorSkill(sk *llm.Skill, envClient *llm.Client, envClientErr error) (*llm.Skill, error) {
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

func collectSkillSummaries(skills map[string]*llm.Skill) []orchestratorSkillSummary {
	out := make([]orchestratorSkillSummary, 0, len(skills))
	for name, sk := range skills {
		summary := orchestratorSkillSummary{Name: name}
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
