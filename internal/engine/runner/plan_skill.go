package runner

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tsumina/dango/internal/llm"
)

const (
	defaultReviewEffort llm.ReasoningEffort = llm.ReasoningEffortMedium
	defaultReplanEffort llm.ReasoningEffort = llm.ReasoningEffortMedium
)

type plannerSkillReviewPrompt struct {
	Mode     string `json:"mode"`
	Task     string `json:"task"`
	Contract string `json:"contract"`
	Data     struct {
		Plan            *CoarsePlan    `json:"plan"`
		PolishFragments map[string]any `json:"polish_fragments,omitempty"`
	} `json:"data"`
}

type plannerSkillReplanPrompt struct {
	Mode     string `json:"mode"`
	Task     string `json:"task"`
	Contract string `json:"contract"`
	Data     struct {
		Request         string         `json:"request"`
		CurrentPlan     *CoarsePlan    `json:"current_plan,omitempty"`
		ReplanReason    string         `json:"replan_reason,omitempty"`
		PolishFragments map[string]any `json:"polish_fragments,omitempty"`
		Skills          []SkillSummary `json:"skills"`
	} `json:"data"`
}

type plannerSkillReplanResponse struct {
	Plan *CoarsePlan `json:"plan,omitempty"`
}

func (r *Runner) reviewPolishedPlan(ctx context.Context) (*PlanReview, error) {
	if r.plannerSkill == nil {
		return nil, fmt.Errorf("orchestrate: runner planner skill is not configured")
	}
	prompt, err := marshalPlannerReviewInput(r.Plan(), r.PolishFragments())
	if err != nil {
		return nil, fmt.Errorf("orchestrate: marshal review input: %w", err)
	}
	raw, err := r.plannerSkill.Run(r.runtimeContext(ctx), prompt, defaultReviewEffort)
	if err != nil {
		return nil, err
	}
	decision, err := parsePlannerReviewOutput(raw)
	if err != nil {
		return nil, fmt.Errorf("orchestrate: %w", err)
	}
	return decision, nil
}

func (r *Runner) replanPolishedPlan(ctx context.Context, reason string) (*CoarsePlan, map[string]*Node, error) {
	if r.plannerSkill == nil {
		return nil, nil, fmt.Errorf("orchestrate: runner planner skill is not configured")
	}
	if r.planNodeBuilder == nil {
		return nil, nil, fmt.Errorf("orchestrate: runner plan node builder is not configured")
	}
	currentPlan := r.Plan()
	prompt, err := marshalPlannerReplanInput(currentPlan.Request, currentPlan, reason, r.PolishFragments(), r.skillSummaries)
	if err != nil {
		return nil, nil, fmt.Errorf("orchestrate: marshal replan input: %w", err)
	}
	raw, err := r.plannerSkill.Run(r.runtimeContext(ctx), prompt, defaultReplanEffort)
	if err != nil {
		return nil, nil, err
	}
	plan, err := parsePlannerReplanOutput(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("orchestrate: %w", err)
	}
	nodes, err := r.planNodeBuilder(plan)
	if err != nil {
		return nil, nil, err
	}
	return plan, nodes, nil
}

func marshalPlannerReviewInput(plan *CoarsePlan, polishFragments map[string]any) (string, error) {
	var payload plannerSkillReviewPrompt
	payload.Mode = "review"
	payload.Task = "Review the current plan against the collected polish fragments. Return strict JSON as {\"approved\":true} or {\"approved\":false,\"reason\":\"...\"}."
	payload.Contract = "If approved is false, reason must explain the replan request briefly and concretely."
	payload.Data.Plan = CloneCoarsePlan(plan)
	payload.Data.PolishFragments = clonePlannerAnyMap(polishFragments)
	return marshalPlannerPrompt(payload)
}

func parsePlannerReviewOutput(raw string) (*PlanReview, error) {
	var out PlanReview
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse review output: %w", err)
	}
	if !out.Approved && out.Reason == "" {
		return nil, fmt.Errorf("review rejected the plan without a reason")
	}
	return &out, nil
}

func marshalPlannerReplanInput(request string, currentPlan *CoarsePlan, reason string, polishFragments map[string]any, skills []SkillSummary) (string, error) {
	var payload plannerSkillReplanPrompt
	payload.Mode = "replan"
	payload.Task = "Rewrite the current plan into the next candidate plan using the rejection reason and the available skills. Return strict JSON as {\"plan\": ...}."
	payload.Contract = "plan.request must be the original request; plan.nodes[].skill_name must reference an available skill; include only the nodes needed for the revised plan."
	payload.Data.Request = request
	payload.Data.CurrentPlan = CloneCoarsePlan(currentPlan)
	payload.Data.ReplanReason = reason
	payload.Data.PolishFragments = clonePlannerAnyMap(polishFragments)
	payload.Data.Skills = append([]SkillSummary(nil), skills...)
	return marshalPlannerPrompt(payload)
}

func parsePlannerReplanOutput(raw string) (*CoarsePlan, error) {
	var out plannerSkillReplanResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse replan output: %w", err)
	}
	if out.Plan == nil {
		return nil, fmt.Errorf("replanner returned no plan")
	}
	return out.Plan, nil
}

func marshalPlannerPrompt(v any) (string, error) {
	buf, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func clonePlannerAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
