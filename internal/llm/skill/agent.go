package skill

import (
	"context"
	"fmt"

	"github.com/tsumina/dango/internal/llm"
)

// DefaultMaxSteps bounds the number of tool-call/response iterations an
// [Agent] will perform before giving up. It re-exports
// [llm.DefaultMaxSteps] so skill callers do not need a direct
// dependency on the llm package for the common case.
const DefaultMaxSteps = llm.DefaultMaxSteps

// Agent drives a tool-using conversation with an LLM.
//
// The Agent wires together:
//
//   - a skill prompt and user input (turned into a [llm.Conversation]),
//   - a set of [Tool] implementations advertised to the model,
//   - the execution loop in [llm.Conversation.Run] that dispatches each
//     requested tool call and feeds the result back into the next
//     request.
//
// Agents are cheap to construct and safe for concurrent use as long as the
// underlying [Tool] implementations are also safe for concurrent use. The
// zero value is not usable; construct one with [NewAgent].
type Agent struct {
	client     *llm.Client
	tools      []llm.Tool
	maxSteps   int
	autoShrink llm.AutoShrinkConfig
	hasShrink  bool
	summarizer llm.Summarizer
	sessStore  llm.SessionStore
	sessID     string
}

// AgentOption customizes [NewAgent].
type AgentOption func(*Agent)

// WithMaxSteps overrides the default iteration bound. Values less than or
// equal to zero are ignored.
func WithMaxSteps(n int) AgentOption {
	return func(a *Agent) {
		if n > 0 {
			a.maxSteps = n
		}
	}
}

// WithAutoTrim configures the agent's [llm.Conversation] to shrink its
// history automatically when the last request's input tokens cross
// cfg.ContextWindow * cfg.Threshold. The policy is applied to every
// conversation built by [Agent.Run].
func WithAutoTrim(cfg llm.AutoShrinkConfig) AgentOption {
	return func(a *Agent) {
		a.autoShrink = cfg
		a.hasShrink = true
	}
}

// WithSummarizer registers a [llm.Summarizer] used by the auto-shrink
// pass to collapse old history into a single summary turn instead of
// dropping it. Without a summarizer the auto-shrink pass falls back to
// trimming.
func WithSummarizer(s llm.Summarizer) AgentOption {
	return func(a *Agent) { a.summarizer = s }
}

// WithSession binds the agent to a persistent session identified by id
// in store. On the first call to [Agent.Run] the agent tries to load the
// session; if it does not exist a new conversation is started using the
// Run instructions and advertised tool schema as the anchor. On every
// subsequent Run the saved conversation is reused verbatim, preserving
// the provider's prompt cache across process restarts. The session is
// written back to store at the end of each Run (both on success and on
// error) so mid-conversation crashes still persist the work done so far.
func WithSession(store llm.SessionStore, id string) AgentOption {
	return func(a *Agent) {
		a.sessStore = store
		a.sessID = id
	}
}

// NewAgent creates an [Agent] bound to client and the given tools.
//
// client must be non-nil. Tool names must be unique; duplicates cause
// NewAgent to return an error so misconfigured tool sets fail fast rather
// than silently shadowing each other at call time.
func NewAgent(client *llm.Client, tools []llm.Tool, opts ...AgentOption) (*Agent, error) {
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
	a := &Agent{
		client:   client,
		tools:    tools,
		maxSteps: DefaultMaxSteps,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a, nil
}

// Tools returns the tools advertised by this agent in registration order.
func (a *Agent) Tools() []llm.Tool { return a.tools }

// Run drives a single task to completion by delegating the
// request/tool-call loop to [llm.Conversation.Run].
//
// instructions is inserted as the system-level instruction for every
// request in the loop (commonly the skill's Instruction body).
// userInput is the initial user message describing the task. The
// returned string is the concatenated output_text of the final
// response.
//
// When [WithSession] is set, Run binds the conversation to the
// configured session on the store and replays any existing event log;
// the instructions argument is ignored in that case so the cache anchor
// does not shift between runs. Every mutation the conversation performs
// during Run is appended to the log automatically.
func (a *Agent) Run(ctx context.Context, instructions, userInput string) (string, error) {
	conv, err := a.openConversation(ctx, instructions)
	if err != nil {
		return "", err
	}
	return conv.Run(ctx, userInput)
}

// openConversation builds the [llm.Conversation] that Run will drive,
// binding it to the configured session store when [WithSession] is set.
// When no session is configured it returns a fresh conversation.
func (a *Agent) openConversation(ctx context.Context, instructions string) (*llm.Conversation, error) {
	conv := a.newConversation(instructions)
	if a.sessStore == nil {
		return conv, nil
	}
	if err := conv.OpenSession(ctx, a.sessStore, a.sessID); err != nil {
		return nil, fmt.Errorf("skill: open session %q: %w", a.sessID, err)
	}
	return conv, nil
}

// newConversation builds a fresh conversation anchored on instructions
// and the agent's registered tools, applying the agent's auto-shrink,
// summariser, and max-steps options.
func (a *Agent) newConversation(instructions string) *llm.Conversation {
	conv := llm.NewConversation(a.client, instructions, a.tools)
	conv.SetMaxSteps(a.maxSteps)
	if a.hasShrink {
		conv.SetAutoShrink(a.autoShrink)
	}
	if a.summarizer != nil {
		conv.SetSummarizer(a.summarizer)
	}
	return conv
}
