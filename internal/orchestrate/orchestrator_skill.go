package orchestrate

import (
	"embed"
	"fmt"
	"io/fs"

	"github.com/tsumina/dango/internal/llm"
)

//go:embed builtin
var embeddedOrchestratorSkillFS embed.FS

func defaultOrchestratorSkill() *llm.Skill {
	sub, err := fs.Sub(embeddedOrchestratorSkillFS, "builtin")
	if err != nil {
		panic(fmt.Sprintf("orchestrate: load embedded orchestrator skill filesystem: %v", err))
	}
	sk, err := llm.NewFromFS(sub, nil, nil)
	if err != nil {
		panic(fmt.Sprintf("orchestrate: load embedded orchestrator skill: %v", err))
	}
	return sk
}

// NewEmbeddedOrchestratorSkill returns the built-in orchestrator skill bound
// with the provided runtime dependencies for execution.
//
// It is the public entrypoint for callers that want the embedded planning and
// review prompt but need to provide their own runtime client, conversation
// configuration, or session wiring.
func NewEmbeddedOrchestratorSkill(client *llm.Client, cfg *llm.ConversationConfig, sessID *string, sessStores ...llm.SessionStore) (*llm.Skill, error) {
	return defaultOrchestratorSkill().Bind(client, cfg, sessID, sessStores...)
}
