package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

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

func planWithOrchestrator(ctx context.Context, req Request, skills []runnerpkg.SkillSummary, runtimeSkill *llm.Skill, requestStream *streampkg.Stream) (*CoarsePlan, *RejectReason, error) {
	prompt, err := marshalOrchestratorPlanningInput(req.Input, skills)
	if err != nil {
		return nil, nil, fmt.Errorf("orchestrate: marshal planner input: %w", err)
	}
	ctx = normalizeContext(ctx)
	raw, err := runtimeSkill.Run(ctx, prompt, defaultPlanningEffort)
	if err != nil {
		return nil, nil, err
	}
	plan, reject, parseErr := parseOrchestratorPlanningOutput(raw)
	if parseErr != nil {
		raw, err = runtimeSkill.Run(ctx, runnerpkg.PlannerRetryPrompt(parseErr), defaultPlanningEffort)
		if err != nil {
			return nil, nil, err
		}
		plan, reject, parseErr = parseOrchestratorPlanningOutput(raw)
		if parseErr != nil {
			return nil, nil, fmt.Errorf("orchestrate: %w (after one retry)", parseErr)
		}
	}
	if handoff, err := planningHandoffMarkdown(req.Input, runtimeSkill, raw); err != nil {
		return nil, nil, fmt.Errorf("orchestrate: build planning handoff: %w", err)
	} else if handoff != "" {
		emitEngineStreamEvent(ctx, requestStream,
			streamSourceOrchestrator(),
			streampkg.EventLLMOutputDelta,
			streampkg.StatusCompleted,
			handoff,
			streampkg.Scope{},
			map[string]any{"stage": "plan"},
		)
	}
	emitEngineStreamEvent(ctx, requestStream,
		streamSourceOrchestrator(),
		streampkg.EventStatusCompleted,
		streampkg.StatusCompleted,
		"orchestrator planning completed",
		streampkg.Scope{},
		map[string]any{"stage": "planning"},
	)
	return plan, reject, nil
}

func planningHandoffMarkdown(request string, runtimeSkill *llm.Skill, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	body := strings.TrimSpace(raw)
	if reasoning := strings.TrimSpace(latestReasoning(runtimeSkill)); reasoning != "" {
		body = body + "\n\nReasoning:\n" + reasoning
	}
	doc := runnerpkg.HandoffDoc{
		RunnerID:  "orchestrator-plan",
		FromNode:  "orchestrator",
		ToNodes:   []string{"runner.bootstrap"},
		Intent:    "plan",
		CreatedAt: time.Now(),
		Body: strings.TrimSpace(fmt.Sprintf("Request:\n%s\n\nPlan:\n%s",
			request,
			body,
		)),
	}
	return doc.Markdown()
}

func marshalOrchestratorPlanningInput(request string, skills []runnerpkg.SkillSummary) (string, error) {
	var payload orchestratorSkillPlanPrompt
	payload.Mode = "plan"
	payload.Task = "Plan the user request using only the listed skills. Reply with one strict JSON object: either {\"plan\":{...}} or {\"reject\":{...}}. No fences, no commentary."
	payload.Contract = "plan.request must repeat the user's original request verbatim. Each plan.nodes[] needs a snake_case id, a skill_name from data.skills, a self-contained task_description (what to do, inputs, output format, constraints, success criteria), and depends_on for any upstream-consuming node. reject must include summary, analysis, and missing_skills when the gap is a missing capability."
	payload.Data.Request = request
	payload.Data.Skills = append([]runnerpkg.SkillSummary(nil), skills...)
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func parseOrchestratorPlanningOutput(raw string) (*runnerpkg.CoarsePlan, *RejectReason, error) {
	cleaned := runnerpkg.ExtractJSONObject(raw)
	if cleaned == "" {
		return nil, nil, fmt.Errorf("parse plan output: planner returned no JSON object (raw=%q)", runnerpkg.SummarizeRaw(raw, 200))
	}
	var out orchestratorSkillPlanResponse
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return nil, nil, fmt.Errorf("parse plan output: %w (raw=%q)", err, runnerpkg.SummarizeRaw(raw, 200))
	}
	if out.Plan != nil && out.Reject != nil {
		return nil, nil, fmt.Errorf("planner returned both a plan and a reject reason")
	}
	if out.Plan == nil && out.Reject == nil {
		return nil, nil, fmt.Errorf("planner returned neither a plan nor a reject reason (raw=%q)", runnerpkg.SummarizeRaw(raw, 200))
	}
	return out.Plan, out.Reject, nil
}

func runtimeOrchestrator(sk *llm.Skill, envClient *llm.Client, envClientErr error, cfg llm.ConversationConfig) (*llm.Skill, error) {
	if sk == nil {
		return nil, errOrchestratorSkillUnconfigured
	}
	if sk.Client() != nil {
		return bindOrchestratorSkillWithConfig(sk, sk.Client(), cfg)
	}
	if envClientErr == nil && envClient != nil {
		return bindOrchestratorSkillWithConfig(sk, envClient, cfg)
	}
	return nil, errOrchestratorSkillUnconfigured
}

func bindOrchestratorSkill(sk *llm.Skill, client *llm.Client) (*llm.Skill, error) {
	return bindOrchestratorSkillWithConfig(sk, client, llm.ConversationConfig{})
}

func bindOrchestratorSkillWithConfig(sk *llm.Skill, client *llm.Client, cfg llm.ConversationConfig) (*llm.Skill, error) {
	return sk.Bind(client, cfg)
}

func planningConversationConfig() llm.ConversationConfig {
	cfg := llm.DefaultConversationConfig()
	cfg.StreamEvents = true
	cfg.StreamSource = streamSourceOrchestrator()
	cfg.StreamMetadata = map[string]any{
		"stage": "planning",
	}
	return cfg
}

func runnerPlannerConversationConfig() llm.ConversationConfig {
	cfg := llm.DefaultConversationConfig()
	cfg.StreamEvents = true
	cfg.StreamSource = streamSourceOrchestrator()
	return cfg
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
