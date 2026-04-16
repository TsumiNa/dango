package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/terrai/skillframe/llm"
	"github.com/terrai/skillframe/registry"
	"github.com/terrai/skillframe/skill"
	"go.uber.org/zap"
)

// Config controls executor behavior.
type Config struct {
	Model          string  // LLM model ID (e.g. "claude-sonnet-4-20250514")
	MaxTokens      int     // max output tokens
	MatchThreshold float64 // minimum skill match score (default 0.1)
	MaxSkills      int     // max skills to inject (default 3)
	SystemPrompt   string  // base system prompt prepended before skills
}

func (c *Config) defaults() {
	if c.Model == "" {
		c.Model = "claude-sonnet-4-20250514"
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 4096
	}
	if c.MatchThreshold == 0 {
		c.MatchThreshold = 0.1
	}
	if c.MaxSkills == 0 {
		c.MaxSkills = 3
	}
}

// Executor orchestrates skill selection and LLM invocation.
type Executor struct {
	client   llm.Client
	registry *registry.Registry
	config   Config
	log      *zap.Logger
}

// New creates an Executor.
func New(client llm.Client, reg *registry.Registry, cfg Config, log *zap.Logger) *Executor {
	cfg.defaults()
	if log == nil {
		log = zap.NewNop()
	}
	return &Executor{
		client:   client,
		registry: reg,
		config:   cfg,
		log:      log,
	}
}

// Run processes a user query:
//  1. Matches relevant skills from the registry
//  2. Builds a system prompt with skill instructions
//  3. Calls the LLM and returns the response
func (e *Executor) Run(ctx context.Context, userQuery string) (*Result, error) {
	// Phase 1: skill selection
	matched := e.registry.Match(userQuery, e.config.MatchThreshold)
	if len(matched) > e.config.MaxSkills {
		matched = matched[:e.config.MaxSkills]
	}

	var usedSkills []string
	for _, m := range matched {
		usedSkills = append(usedSkills, fmt.Sprintf("%s (%.2f)", m.Skill.Name, m.Score))
	}
	e.log.Info("matched skills", zap.Strings("skills", usedSkills))

	// Phase 2: compose prompt
	system := e.buildSystemPrompt(matched)

	messages := []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: userQuery},
	}

	// Phase 3: LLM call
	resp, err := e.client.Complete(ctx, &llm.Request{
		Model:     e.config.Model,
		Messages:  messages,
		MaxTokens: e.config.MaxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("llm complete: %w", err)
	}

	skills := make([]*skill.Skill, len(matched))
	for i, m := range matched {
		skills[i] = m.Skill
	}

	return &Result{
		Response:   resp,
		UsedSkills: skills,
	}, nil
}

// RunWithSkill forces a specific skill to be used (bypass matching).
func (e *Executor) RunWithSkill(ctx context.Context, userQuery string, skillName string) (*Result, error) {
	s, ok := e.registry.Get(skillName)
	if !ok {
		return nil, fmt.Errorf("skill not found: %s", skillName)
	}

	matched := []registry.ScoredSkill{{Skill: s, Score: 1.0}}
	system := e.buildSystemPrompt(matched)

	messages := []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: userQuery},
	}

	resp, err := e.client.Complete(ctx, &llm.Request{
		Model:     e.config.Model,
		Messages:  messages,
		MaxTokens: e.config.MaxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("llm complete: %w", err)
	}

	return &Result{
		Response:   resp,
		UsedSkills: []*skill.Skill{s},
	}, nil
}

func (e *Executor) buildSystemPrompt(matched []registry.ScoredSkill) string {
	var sb strings.Builder

	// Base system prompt
	if e.config.SystemPrompt != "" {
		sb.WriteString(e.config.SystemPrompt)
		sb.WriteString("\n\n")
	}

	if len(matched) == 0 {
		return sb.String()
	}

	// Available skills catalog (Level 1: metadata only)
	sb.WriteString("<available_skills>\n")
	for _, s := range e.registry.All() {
		sb.WriteString(fmt.Sprintf("- **%s**: %s\n", s.Name, s.Description))
	}
	sb.WriteString("</available_skills>\n\n")

	// Active skill instructions (Level 2: full body)
	sb.WriteString("<active_skills>\n")
	for _, m := range matched {
		sb.WriteString(fmt.Sprintf("<skill name=\"%s\">\n", m.Skill.Name))
		sb.WriteString(m.Skill.Body)
		sb.WriteString("\n</skill>\n\n")
	}
	sb.WriteString("</active_skills>\n")

	return sb.String()
}

// Result holds the output of an executor run.
type Result struct {
	Response   *llm.Response
	UsedSkills []*skill.Skill
}
