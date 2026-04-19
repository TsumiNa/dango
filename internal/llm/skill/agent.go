package skill

import (
	"context"
	"fmt"

	"github.com/tsumina/dango/internal/llm"
)

// DefaultMaxSteps re-exports [llm.DefaultMaxSteps] so skill callers do
// not need a direct dependency on the llm package for the common case.
const DefaultMaxSteps = llm.DefaultMaxSteps

// Agent is the top-level entry point for running a skill.
//
// An Agent is a thin wrapper around a single [llm.Conversation]: the
// Conversation owns the request/tool-call loop, the optional session
// log, and the tool dispatch table. The Agent itself only carries the
// deferred session binding so a session can be opened on first [Agent.Run]
// rather than at construction time (which would require a context).
//
// The zero value is not usable; construct one with [NewAgent].
type Agent struct {
	conv      *llm.Conversation
	sessStore llm.SessionStore
	sessID    string
}

// AgentOption customizes [NewAgent]. Options are applied in registration
// order to the underlying [llm.Conversation] (or to session binding
// fields on the Agent for [WithSession]).
type AgentOption func(*Agent)

// WithMaxSteps overrides the iteration bound used by the
// conversation's run loop. Values less than or equal to zero are
// ignored; [llm.DefaultMaxSteps] remains the fallback.
func WithMaxSteps(n int) AgentOption {
	return func(a *Agent) { a.conv.SetMaxSteps(n) }
}

// WithAutoTrim configures the conversation to shrink its history
// automatically when the last request's input tokens cross
// cfg.ContextWindow * cfg.Threshold.
func WithAutoTrim(cfg llm.AutoShrinkConfig) AgentOption {
	return func(a *Agent) { a.conv.SetAutoShrink(cfg) }
}

// WithSummarizer registers a [llm.Summarizer] used by the auto-shrink
// pass to collapse old history into a single summary turn instead of
// dropping it. Without a summarizer the auto-shrink pass falls back to
// trimming.
func WithSummarizer(s llm.Summarizer) AgentOption {
	return func(a *Agent) { a.conv.SetSummarizer(s) }
}

// WithSession binds the agent to a persistent session identified by id
// in store. The session is opened on the first call to [Agent.Run]; if
// it does not exist the initial event log is seeded from the
// construction-time instructions and tools, otherwise the existing log
// is replayed. Every mutation performed during a Run is appended to the
// log automatically.
func WithSession(store llm.SessionStore, id string) AgentOption {
	return func(a *Agent) {
		a.sessStore = store
		a.sessID = id
	}
}

// NewAgent builds an [Agent] around a fresh [llm.Conversation] anchored
// on instructions and tools.
//
// client must be non-nil. Tool names must be unique and non-empty;
// duplicates cause NewAgent to return an error so misconfigured tool
// sets fail fast rather than silently shadowing each other at call
// time.
func NewAgent(client *llm.Client, instructions string, tools []llm.Tool, opts ...AgentOption) (*Agent, error) {
	if client == nil {
		return nil, fmt.Errorf("skill: agent requires a non-nil client")
	}
	seen := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("skill: agent received nil tool")
		}
		name := t.Name()
		if name == "" {
			return nil, fmt.Errorf("skill: tool has empty name")
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("skill: duplicate tool name %q", name)
		}
		seen[name] = struct{}{}
	}
	a := &Agent{conv: llm.NewConversation(client, instructions, tools)}
	for _, opt := range opts {
		opt(a)
	}
	return a, nil
}

// Conversation returns the underlying [llm.Conversation]. Callers may
// inspect its turns, usage, or session metadata but should not mutate
// it concurrently with a running [Agent.Run].
func (a *Agent) Conversation() *llm.Conversation { return a.conv }

// Run drives a single task to completion. It lazily binds the
// conversation to the configured session on the first call when
// [WithSession] was provided, then delegates to [llm.Conversation.Run].
//
// userInput is appended as a user turn before the loop starts. The
// returned string is the concatenated output_text of the model's final
// response.
func (a *Agent) Run(ctx context.Context, userInput string) (string, error) {
	if a.sessStore != nil && a.conv.SessionID() == "" {
		if err := a.conv.OpenSession(ctx, a.sessStore, a.sessID); err != nil {
			return "", fmt.Errorf("skill: open session %q: %w", a.sessID, err)
		}
	}
	return a.conv.Run(ctx, userInput)
}
