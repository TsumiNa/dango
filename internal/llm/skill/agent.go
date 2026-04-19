package skill

import (
	"context"
	"fmt"

	"github.com/tsumina/dango/internal/llm"
)

// DefaultMaxSteps bounds the number of tool-call/response iterations an
// [Agent] will perform before giving up. It protects against runaway loops
// when the model keeps requesting tool calls without producing a final
// message.
const DefaultMaxSteps = 20

// Agent drives a tool-using conversation with an LLM.
//
// The Agent wires together:
//
//   - a skill prompt and user input (turned into a [llm.Conversation]),
//   - a set of [Tool] implementations advertised to the model,
//   - an execution loop that dispatches each requested tool call and feeds
//     the result back into the next request.
//
// Agents are cheap to construct and safe for concurrent use as long as the
// underlying [Tool] implementations are also safe for concurrent use. The
// zero value is not usable; construct one with [NewAgent].
type Agent struct {
	client     *llm.Client
	tools      []llm.Tool
	toolByName map[string]llm.Tool
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
	byName := make(map[string]llm.Tool, len(tools))
	for _, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("skill: agent received nil tool")
		}
		name := t.Name()
		if name == "" {
			return nil, fmt.Errorf("skill: tool has empty name")
		}
		if _, dup := byName[name]; dup {
			return nil, fmt.Errorf("skill: duplicate tool name %q", name)
		}
		byName[name] = t
	}
	a := &Agent{
		client:     client,
		tools:      tools,
		toolByName: byName,
		maxSteps:   DefaultMaxSteps,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a, nil
}

// Tools returns the tools advertised by this agent in registration order.
func (a *Agent) Tools() []llm.Tool { return a.tools }

// Run drives a single task to completion.
//
// instructions is inserted as the system-level instruction for every request
// in the loop (commonly the skill's Instruction body). userInput is the
// initial user message describing the task. Run builds a
// [llm.Conversation] anchored on instructions and the advertised tool
// schema, appends userInput as a user turn, then repeatedly calls
// [llm.Conversation.Send] and dispatches any function tool calls the model emits
// via the registered [Tool] instances until the model produces a final
// text response or the step budget is exhausted. The returned string is
// the concatenated output_text of the final response.
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
	conv.AppendUser(userInput)

	for step := 0; step < a.maxSteps; step++ {
		resp, err := conv.Send(ctx)
		if err != nil {
			return "", fmt.Errorf("skill: agent request failed at step %d: %w", step, err)
		}
		if len(resp.ToolCalls) == 0 {
			if lastErr := conv.LastError(); lastErr != nil {
				return resp.Text, fmt.Errorf("skill: session persistence failed: %w", lastErr)
			}
			return resp.Text, nil
		}
		for _, call := range resp.ToolCalls {
			output, execErr := a.dispatch(ctx, call)
			conv.AppendToolOutput(call.CallID, output, execErr)
			// execErr is surfaced to the model via output so the loop
			// can recover on the next turn.
		}
	}
	return "", fmt.Errorf("skill: agent exceeded max steps (%d) without final response", a.maxSteps)
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
// and the agent's advertised tool schema, applying the agent's
// auto-shrink and summariser options.
func (a *Agent) newConversation(instructions string) *llm.Conversation {
	conv := llm.NewConversation(a.client, instructions, a.toolSpecs())
	if a.hasShrink {
		conv.SetAutoShrink(a.autoShrink)
	}
	if a.summarizer != nil {
		conv.SetSummarizer(a.summarizer)
	}
	return conv
}

// dispatch executes a single function call against the registered tools.
// On error the error text is returned as output so the model sees it.
func (a *Agent) dispatch(ctx context.Context, call llm.ToolCall) (string, error) {
	tool, ok := a.toolByName[call.Name]
	if !ok {
		msg := fmt.Sprintf("error: unknown tool %q", call.Name)
		return msg, fmt.Errorf("skill: unknown tool %q", call.Name)
	}
	out, err := tool.Execute(ctx, call.Arguments)
	if err != nil {
		msg := fmt.Sprintf("error: %s\n%s", err.Error(), out)
		return msg, err
	}
	return out, nil
}

// toolSpecs converts the registered [Tool] set into the provider-agnostic
// [llm.ToolSpec] slice consumed by [llm.NewConversation].
func (a *Agent) toolSpecs() []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(a.tools))
	for _, t := range a.tools {
		out = append(out, llm.ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}
	return out
}
