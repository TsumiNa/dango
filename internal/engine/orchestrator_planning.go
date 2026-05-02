package engine

import (
	"context"
	"encoding/json"
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

func planWithOrchestrator(ctx context.Context, req Request, skills []runnerpkg.SkillSummary, runtimeSkill *llm.Skill, eventStream *streampkg.Stream) (*CoarsePlan, *RejectReason, error) {
	prompt, err := marshalOrchestratorPlanningInput(req.Input, skills)
	if err != nil {
		return nil, nil, fmt.Errorf("orchestrate: marshal planner input: %w", err)
	}
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
	plan, reject, err := parseOrchestratorPlanningOutput(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("orchestrate: %w", err)
	}
	return plan, reject, nil
}

func marshalOrchestratorPlanningInput(request string, skills []runnerpkg.SkillSummary) (string, error) {
	var payload orchestratorSkillPlanPrompt
	payload.Mode = "plan"
	payload.Task = "Plan the request using the available skills. Return strict JSON with exactly one of {\"plan\": ...} or {\"reject\": ...}."
	payload.Contract = "plan.request must be the original request; plan.nodes[].skill_name must reference an available skill; reject must include summary and analysis."
	payload.Data.Request = request
	payload.Data.Skills = append([]runnerpkg.SkillSummary(nil), skills...)
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(buf), nil
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
