package engine

import (
	"encoding/json"
	"fmt"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
)

type orchestratorSkillPlanResponse struct {
	Plan   *runnerpkg.CoarsePlan `json:"plan,omitempty"`
	Reject *RejectReason         `json:"reject,omitempty"`
}

type orchestratorSkillPlanPrompt struct {
	Mode     string `json:"mode"`
	Task     string `json:"task"`
	Contract string `json:"contract"`
	Data     struct {
		Request string                   `json:"request"`
		Skills  []runnerpkg.SkillSummary `json:"skills"`
	} `json:"data"`
}

func marshalOrchestratorPlanningInput(request string, skills []runnerpkg.SkillSummary) (string, error) {
	var payload orchestratorSkillPlanPrompt
	payload.Mode = "plan"
	payload.Task = "Plan the request using the available skills. Return strict JSON with exactly one of {\"plan\": ...} or {\"reject\": ...}."
	payload.Contract = "plan.request must be the original request; plan.nodes[].skill_name must reference an available skill; reject must include summary and analysis."
	payload.Data.Request = request
	payload.Data.Skills = append([]runnerpkg.SkillSummary(nil), skills...)
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

func marshalOrchestratorPrompt(v any) (string, error) {
	buf, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}
