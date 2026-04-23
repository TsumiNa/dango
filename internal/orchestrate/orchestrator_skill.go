package orchestrate

import (
	"embed"
	"fmt"

	"github.com/tsumina/dango/internal/llm/skill"
)

//go:embed builtin/SKILL.md
var embeddedOrchestratorSkillFS embed.FS

func defaultOrchestratorSkill() *skill.Skill {
	sk, err := skill.LoadFS(embeddedOrchestratorSkillFS, "builtin")
	if err != nil {
		panic(fmt.Sprintf("orchestrate: load embedded orchestrator skill: %v", err))
	}
	return sk
}

// NewEmbeddedOrchestratorSkill returns the built-in orchestrator skill bound
// with cfg for execution.
//
// It is the public entrypoint for callers that want the embedded planning and
// review prompt but need to provide their own runtime client, tools, or
// session wiring.
func NewEmbeddedOrchestratorSkill(cfg skill.RuntimeConfig) (*skill.Skill, error) {
	return defaultOrchestratorSkill().Bind(cfg)
}
