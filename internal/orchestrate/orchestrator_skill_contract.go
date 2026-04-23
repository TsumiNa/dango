package orchestrate

import (
	"encoding/json"
	"fmt"

	runnerpkg "github.com/tsumina/dango/internal/orchestrate/runner"
)

type orchestratorSkillSummary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type orchestratorSkillPlanResponse struct {
	Plan   *runnerpkg.CoarsePlan `json:"plan,omitempty"`
	Reject *RejectReason         `json:"reject,omitempty"`
}

type orchestratorSkillPlanPrompt struct {
	Mode     string `json:"mode"`
	Task     string `json:"task"`
	Contract string `json:"contract"`
	Data     struct {
		Request string                     `json:"request"`
		Skills  []orchestratorSkillSummary `json:"skills"`
	} `json:"data"`
}

type orchestratorSkillReviewPrompt struct {
	Mode     string `json:"mode"`
	Task     string `json:"task"`
	Contract string `json:"contract"`
	Data     struct {
		Plan            *runnerpkg.CoarsePlan `json:"plan"`
		PolishFragments map[string]any        `json:"polish_fragments,omitempty"`
	} `json:"data"`
}

type orchestratorSkillReplanPrompt struct {
	Mode     string `json:"mode"`
	Task     string `json:"task"`
	Contract string `json:"contract"`
	Data     struct {
		Request         string                     `json:"request"`
		CurrentPlan     *runnerpkg.CoarsePlan      `json:"current_plan,omitempty"`
		ReplanReason    string                     `json:"replan_reason,omitempty"`
		PolishFragments map[string]any             `json:"polish_fragments,omitempty"`
		Skills          []orchestratorSkillSummary `json:"skills"`
	} `json:"data"`
}

type orchestratorSkillReplanResponse struct {
	Plan *runnerpkg.CoarsePlan `json:"plan,omitempty"`
}

func marshalOrchestratorPlanningInput(request string, skills []orchestratorSkillSummary) (string, error) {
	var payload orchestratorSkillPlanPrompt
	payload.Mode = "plan"
	payload.Task = "Plan the request using the available skills. Return strict JSON with exactly one of {\"plan\": ...} or {\"reject\": ...}."
	payload.Contract = "plan.request must be the original request; plan.nodes[].skill_name must reference an available skill; reject must include summary and analysis."
	payload.Data.Request = request
	payload.Data.Skills = append([]orchestratorSkillSummary(nil), skills...)
	return marshalOrchestratorPrompt(payload)
}

func parseOrchestratorPlanningOutput(raw string) (*runnerpkg.CoarsePlan, *RejectReason, error) {
	var out orchestratorSkillPlanResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, nil, fmt.Errorf("parse plan output: %w", err)
	}
	if out.Plan != nil && out.Reject != nil {
		return nil, nil, fmt.Errorf("planner returned both a plan and a reject reason")
	}
	if out.Plan == nil && out.Reject == nil {
		return nil, nil, fmt.Errorf("planner returned neither a plan nor a reject reason")
	}
	return out.Plan, out.Reject, nil
}

func marshalOrchestratorReviewInput(plan *runnerpkg.CoarsePlan, polishFragments map[string]any) (string, error) {
	var payload orchestratorSkillReviewPrompt
	payload.Mode = "review"
	payload.Task = "Review the current plan against the collected polish fragments. Return strict JSON as {\"approved\":true} or {\"approved\":false,\"reason\":\"...\"}."
	payload.Contract = "If approved is false, reason must explain the replan request briefly and concretely."
	payload.Data.Plan = runnerpkg.CloneCoarsePlan(plan)
	payload.Data.PolishFragments = cloneOrchestratorAnyMap(polishFragments)
	return marshalOrchestratorPrompt(payload)
}

func parseOrchestratorReviewOutput(raw string) (*PlanReview, error) {
	var out PlanReview
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse review output: %w", err)
	}
	if !out.Approved && out.Reason == "" {
		return nil, fmt.Errorf("review rejected the plan without a reason")
	}
	return &out, nil
}

func marshalOrchestratorReplanInput(request string, currentPlan *runnerpkg.CoarsePlan, reason string, polishFragments map[string]any, skills []orchestratorSkillSummary) (string, error) {
	var payload orchestratorSkillReplanPrompt
	payload.Mode = "replan"
	payload.Task = "Rewrite the current plan into the next candidate plan using the rejection reason and the available skills. Return strict JSON as {\"plan\": ...}."
	payload.Contract = "plan.request must be the original request; plan.nodes[].skill_name must reference an available skill; include only the nodes needed for the revised plan."
	payload.Data.Request = request
	payload.Data.CurrentPlan = runnerpkg.CloneCoarsePlan(currentPlan)
	payload.Data.ReplanReason = reason
	payload.Data.PolishFragments = cloneOrchestratorAnyMap(polishFragments)
	payload.Data.Skills = append([]orchestratorSkillSummary(nil), skills...)
	return marshalOrchestratorPrompt(payload)
}

func parseOrchestratorReplanOutput(raw string) (*runnerpkg.CoarsePlan, error) {
	var out orchestratorSkillReplanResponse
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse replan output: %w", err)
	}
	if out.Plan == nil {
		return nil, fmt.Errorf("replanner returned no plan")
	}
	return out.Plan, nil
}

func marshalOrchestratorPrompt(v any) (string, error) {
	buf, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func cloneOrchestratorAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
