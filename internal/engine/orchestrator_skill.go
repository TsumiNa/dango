package engine

import (
	"embed"
	"fmt"
	"io/fs"

	"github.com/tsumina/dango/internal/llm"
)

//go:embed builtin
var embeddedOrchestratorSkillFS embed.FS

// defaultOrchestratorSkill loads the embedded orchestrator skill with built-in
// workspace tools attached, but no orchestrator-registry tools. It is used by
// callers that hold the skill independently of an [Orchestrator] instance,
// such as direct tests of the skill's planning/review prompts.
func defaultOrchestratorSkill() *llm.Skill {
	sk, err := loadOrchestratorSkill()
	if err != nil {
		panic(fmt.Sprintf("orchestrate: %v", err))
	}
	return sk
}

// loadOrchestratorSkill reads the embedded orchestrator SKILL.md, attaches the
// supplied extra tools (typically registry-introspection helpers built from a
// live Orchestrator), and gives the result the standard skill-workspace tool
// set scoped to the skill's own private temp playground. The embedded prompt
// also opts into list_dir and pwd for scratch-playground inspection.
//
// Built-in tools are scoped to the skill's temp playground, so the orchestrator
// can write scratch notes or run small diagnostic commands while reasoning, but
// it cannot reach outside that playground.
func loadOrchestratorSkill(extraTools ...llm.Tool) (*llm.Skill, error) {
	sub, err := fs.Sub(embeddedOrchestratorSkillFS, "builtin")
	if err != nil {
		return nil, fmt.Errorf("load embedded orchestrator skill filesystem: %w", err)
	}
	var opts []llm.SkillOption
	if len(extraTools) > 0 {
		opts = append(opts, llm.WithTools(extraTools...))
	}
	cfg := llm.DefaultSkillConfig()
	cfg.ToolSet.Extras = []llm.ExtraTool{llm.ExtraListDir, llm.ExtraPwd}
	sk, err := llm.NewSkill(sub, cfg, opts...)
	if err != nil {
		return nil, fmt.Errorf("load embedded orchestrator skill: %w", err)
	}
	sk, err = sk.SetAccessibleDirsAndBuiltinTools()
	if err != nil {
		return nil, fmt.Errorf("attach builtin tools to orchestrator skill: %w", err)
	}
	return sk, nil
}

// NewEmbeddedOrchestratorSkill returns the built-in orchestrator skill bound
// with the provided runtime dependencies for execution.
//
// It is the public entrypoint for callers that want the embedded planning and
// review prompt but need to provide their own runtime client, conversation
// configuration, or session wiring. The returned skill includes the standard
// workspace tool set so the orchestrator can take notes in its own scratch
// playground while reasoning.
func NewEmbeddedOrchestratorSkill(client *llm.Client, cfg llm.ConversationConfig, opts ...llm.BindOption) (*llm.Skill, error) {
	return defaultOrchestratorSkill().Bind(client, cfg, opts...)
}
