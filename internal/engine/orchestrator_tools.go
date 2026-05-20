package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/tsumina/dango/internal/llm"
)

// orchestratorRegistry is the slice of the Orchestrator API that the built-in
// orchestrator-only tools need to introspect the live skill catalogue. Keeping
// it as an interface lets the tools take a closure over any future registry
// implementation without an import cycle, and lets tests pass in a stub.
type orchestratorRegistry interface {
	Skills() map[string]*llm.Skill
}

// orchestratorRegistryTools returns the tool set that only makes sense for the
// embedded orchestrator skill — discovery helpers that let the planner answer
// "what skills do you have?" directly, without drafting a coarse plan first.
//
// Only the public surface (skill name + description) is exposed. The
// orchestrator deliberately cannot read another skill's SKILL.md body or
// internal tool set; that protects agent privacy and is the architectural
// reason the polish stage exists — each agent skill elaborates its own
// capability against a concrete assigned task. A future negotiated
// "detailed_describe" tool can be added if/when the orchestrator needs to
// solicit elaboration from a specific skill before plan time.
//
// The tools capture registry by reference so they always see the live state
// at the moment the model invokes them.
func orchestratorRegistryTools(registry orchestratorRegistry) []llm.Tool {
	if registry == nil {
		return nil
	}
	return []llm.Tool{
		newListSkillsTool(registry),
	}
}

func newListSkillsTool(registry orchestratorRegistry) llm.Tool {
	return llm.NewFuncTool(
		"list_skills",
		"List every domain skill currently registered with the orchestrator. "+
			"Use this when the user asks what skills are available, what this "+
			"system can do, or to confirm a skill name before composing a plan. "+
			"Returns a JSON array of {name, description}. Takes no arguments.",
		map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		func(_ context.Context, _ string) (string, error) {
			summaries := skillSummariesFromRegistry(registry)
			buf, err := json.MarshalIndent(summaries, "", "  ")
			if err != nil {
				return "", fmt.Errorf("list_skills: marshal: %w", err)
			}
			return string(buf), nil
		},
	)
}

func skillSummariesFromRegistry(registry orchestratorRegistry) []map[string]string {
	skills := registry.Skills()
	out := make([]map[string]string, 0, len(skills))
	for name, sk := range skills {
		entry := map[string]string{"name": name}
		if sk != nil {
			entry["description"] = sk.Description
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["name"] < out[j]["name"]
	})
	return out
}
