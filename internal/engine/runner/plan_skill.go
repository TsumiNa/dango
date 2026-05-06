package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
		Plan            *CoarsePlan       `json:"plan"`
		PolishDocuments map[string]string `json:"polish_documents,omitempty"`
	} `json:"data"`
}

type plannerSkillReplanPrompt struct {
	Mode     string `json:"mode"`
	Task     string `json:"task"`
	Contract string `json:"contract"`
	Data     struct {
		Request         string            `json:"request"`
		CurrentPlan     *CoarsePlan       `json:"current_plan,omitempty"`
		ReplanReason    string            `json:"replan_reason,omitempty"`
		PolishDocuments map[string]string `json:"polish_documents,omitempty"`
		Skills          []SkillSummary    `json:"skills"`
	} `json:"data"`
}

type plannerSkillReplanResponse struct {
	Plan *CoarsePlan `json:"plan,omitempty"`
}

type plannerSkillReviewResponse struct {
	Approved *bool  `json:"approved,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Reject   *struct {
		Summary  string `json:"summary,omitempty"`
		Analysis string `json:"analysis,omitempty"`
	} `json:"reject,omitempty"`
}

func (r *Runner) reviewPolishedPlan(ctx context.Context) (*PlanReview, error) {
	if r.plannerSkill == nil {
		return nil, fmt.Errorf("orchestrate: runner planner skill is not configured")
	}
	prompt, err := marshalPlannerReviewInput(r.Plan(), r.PolishFragments())
	if err != nil {
		return nil, fmt.Errorf("orchestrate: marshal review input: %w", err)
	}
	runCtx := r.runtimeContext(ctx)
	raw, err := r.plannerSkill.Run(runCtx, prompt, defaultReviewEffort)
	if err != nil {
		return nil, err
	}
	decision, parseErr := parsePlannerReviewOutput(raw)
	if parseErr != nil {
		raw, err = r.plannerSkill.Run(runCtx, PlannerRetryPrompt(parseErr), defaultReviewEffort)
		if err != nil {
			return nil, err
		}
		decision, parseErr = parsePlannerReviewOutput(raw)
		if parseErr != nil {
			return nil, fmt.Errorf("orchestrate: %w (after one retry)", parseErr)
		}
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
	runCtx := r.runtimeContext(ctx)
	raw, err := r.plannerSkill.Run(runCtx, prompt, defaultReplanEffort)
	if err != nil {
		return nil, nil, err
	}
	plan, parseErr := parsePlannerReplanOutput(raw)
	if parseErr != nil {
		raw, err = r.plannerSkill.Run(runCtx, PlannerRetryPrompt(parseErr), defaultReplanEffort)
		if err != nil {
			return nil, nil, err
		}
		plan, parseErr = parsePlannerReplanOutput(raw)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("orchestrate: %w (after one retry)", parseErr)
		}
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
	payload.Task = "Decide whether the polished plan is good enough to execute. Reply with one strict JSON object: {\"approved\":true} or {\"reject\":{...}}. No fences, no commentary."
	payload.Contract = "Default to approve. Only reject when a polish document explicitly says the assigned task is infeasible for that skill, when polish output contradicts a hard user constraint, or when the plan picks the wrong skill for a node. reject.summary is a short replan reason; reject.analysis names the specific defect."
	payload.Data.Plan = CloneCoarsePlan(plan)
	payload.Data.PolishDocuments = plannerMarkdownDocuments(polishFragments)
	return marshalPlannerPrompt(payload)
}

func parsePlannerReviewOutput(raw string) (*PlanReview, error) {
	cleaned := ExtractJSONObject(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("parse review output: planner returned no JSON object (raw=%q)", SummarizeRaw(raw, 200))
	}
	var out plannerSkillReviewResponse
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return nil, fmt.Errorf("parse review output: %w (raw=%q)", err, SummarizeRaw(raw, 200))
	}
	if out.Reject != nil {
		reason := strings.TrimSpace(out.Reject.Summary)
		analysis := strings.TrimSpace(out.Reject.Analysis)
		switch {
		case reason == "":
			reason = analysis
		case analysis != "":
			reason += ": " + analysis
		}
		return &PlanReview{Approved: false, Reason: reason}, nil
	}
	if out.Approved == nil {
		return nil, fmt.Errorf("review output missing approved or reject (raw=%q)", SummarizeRaw(raw, 200))
	}
	return &PlanReview{Approved: *out.Approved, Reason: strings.TrimSpace(out.Reason)}, nil
}

func marshalPlannerReplanInput(request string, currentPlan *CoarsePlan, reason string, polishFragments map[string]any, skills []SkillSummary) (string, error) {
	var payload plannerSkillReplanPrompt
	payload.Mode = "replan"
	payload.Task = "Revise the current plan to address replan_reason. Reply with one strict JSON object: {\"plan\":{...}}. No fences, no commentary."
	payload.Contract = "Make the smallest change that resolves replan_reason. Preserve node ids that remain valid. plan.request must repeat data.request verbatim; every plan.nodes[].skill_name must reference data.skills; task_description must be self-contained (what to do, inputs, outputs, constraints, success criteria); set depends_on for upstream-consuming nodes."
	payload.Data.Request = request
	payload.Data.CurrentPlan = CloneCoarsePlan(currentPlan)
	payload.Data.ReplanReason = reason
	payload.Data.PolishDocuments = plannerMarkdownDocuments(polishFragments)
	payload.Data.Skills = append([]SkillSummary(nil), skills...)
	return marshalPlannerPrompt(payload)
}

func parsePlannerReplanOutput(raw string) (*CoarsePlan, error) {
	cleaned := ExtractJSONObject(raw)
	if cleaned == "" {
		return nil, fmt.Errorf("parse replan output: planner returned no JSON object (raw=%q)", SummarizeRaw(raw, 200))
	}
	var out plannerSkillReplanResponse
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return nil, fmt.Errorf("parse replan output: %w (raw=%q)", err, SummarizeRaw(raw, 200))
	}
	if out.Plan == nil {
		return nil, fmt.Errorf("replanner returned no plan (raw=%q)", SummarizeRaw(raw, 200))
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

// PlannerRetryPrompt builds a short follow-up turn for the planner skill when
// the previous response could not be parsed. It is sent in the same
// conversation so the model has full context on what it just produced.
func PlannerRetryPrompt(parseErr error) string {
	reason := "the previous response could not be parsed"
	if parseErr != nil {
		reason = parseErr.Error()
	}
	return "Your previous response could not be parsed: " + reason +
		". Reply now with one strict JSON object only — no markdown fences, no language tag, no commentary, no leading or trailing prose. " +
		"Match the JSON contract for the current mode exactly."
}

func plannerMarkdownDocuments(in map[string]any) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = plannerMarkdownDocument(v)
	}
	return out
}

func plannerMarkdownDocument(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case *ExchangeDocument:
		if value == nil {
			return ""
		}
		raw, err := value.Markdown()
		if err == nil {
			return raw
		}
		return fmt.Sprintf("%+v", *value)
	case ExchangeDocument:
		raw, err := value.Markdown()
		if err == nil {
			return raw
		}
		return fmt.Sprintf("%+v", value)
	default:
		buf, err := json.Marshal(value)
		if err == nil {
			return string(buf)
		}
		return fmt.Sprintf("%v", value)
	}
}
