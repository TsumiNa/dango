package orchestrate

import (
	"context"
	"fmt"
	"sort"

	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/llm/skill"
	llmskillbuiltin "github.com/tsumina/dango/internal/llm/skill/builtin"
)

const (
	defaultPlanningEffort llm.ReasoningEffort = llm.ReasoningEffortMedium
	defaultReviewEffort   llm.ReasoningEffort = llm.ReasoningEffortMedium
	defaultReplanEffort   llm.ReasoningEffort = llm.ReasoningEffortMedium
)

func planWithOrchestratorSkill(ctx context.Context, req *Request, skills map[string]*skill.Skill, orchestratorSkill *skill.Skill) (*CoarsePlan, *RejectReason, error) {
	runtimeSkill, err := runtimeOrchestratorSkill(orchestratorSkill)
	if err != nil {
		return rejectUnconfiguredPlan(req, skills, orchestratorSkill)
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

func reviewWithOrchestratorSkill(ctx context.Context, plan *CoarsePlan, polishFragments map[string]any, orchestratorSkill *skill.Skill) (*PlanReview, error) {
	runtimeSkill, err := runtimeOrchestratorSkill(orchestratorSkill)
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

func replanWithOrchestratorSkill(ctx context.Context, request string, currentPlan *CoarsePlan, reason string, polishFragments map[string]any, skills map[string]*skill.Skill, orchestratorSkill *skill.Skill) (*CoarsePlan, error) {
	runtimeSkill, err := runtimeOrchestratorSkill(orchestratorSkill)
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

func runtimeOrchestratorSkill(sk *skill.Skill) (*skill.Skill, error) {
	if sk == nil {
		return nil, fmt.Errorf("orchestrate: orchestrator skill is not configured")
	}
	if client, err := llm.NewClientFromEnv(); err == nil {
		return bindOrchestratorSkill(sk, client)
	}
	if sk.Client() != nil {
		return bindOrchestratorSkill(sk, sk.Client())
	}
	return nil, fmt.Errorf("orchestrate: orchestrator skill has no runnable llm client")
}

func bindOrchestratorSkill(sk *skill.Skill, client *llm.Client) (*skill.Skill, error) {
	if sk.Client() == client && sk.Conversation() != nil {
		return sk, nil
	}
	return sk.Bind(skill.RuntimeConfig{
		Client: client,
		Tools:  orchestratorSkillTools(sk),
	})
}

func orchestratorSkillTools(sk *skill.Skill) []llm.Tool {
	if sk == nil || sk.Dir() == "" {
		return nil
	}
	return llmskillbuiltin.All(sk.Dir(), llmskillbuiltin.WithAllowlistAdjust(sk.BashAllow(), sk.BashBlock()))
}

func collectSkillSummaries(skills map[string]*skill.Skill) []orchestratorSkillSummary {
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
