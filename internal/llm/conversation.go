package llm

import (
	"context"
	"encoding/json"
	"strconv"
	"time"
)

// Role classifies the origin of a [Turn] in a [Conversation].
type Role string

// Role constants covering every kind of entry that can appear in a
// [Conversation]. Additional tiers are intentionally not collapsed here so
// that prompt-cache tiering can treat them differently.
const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolCall   Role = "tool_call"
	RoleToolOutput Role = "tool_output"
)

// Tier groups turns by how likely they are to mutate, which in turn decides
// the order in which automatic shrinking discards them.
type Tier int

// Tier constants listed from most to least stable. Lower numeric values are
// preferred for retention by [Conversation.maybeAutoShrink].
const (
	// TierAnchor marks the immutable prefix (instructions and tool schema)
	// that must never change mid-session so the provider's prompt cache
	// can stay hot.
	TierAnchor Tier = 0
	// TierStableHistory marks user and assistant messages that belong to
	// the running narrative of the conversation.
	TierStableHistory Tier = 1
	// TierToolIO marks tool_call and tool_output entries whose bodies can
	// be summarised aggressively.
	TierToolIO Tier = 2
	// TierVolatile marks the latest user turn that is still actively being
	// worked on and is cheap to evict.
	TierVolatile Tier = 3
)

// ToolSpec is the minimal description of a function tool that is advertised
// to the model. It intentionally stays in the [llm] package so callers do
// not depend on the OpenAI SDK's parameter types.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolCall is a single function call requested by the model.
type ToolCall struct {
	CallID    string
	Name      string
	Arguments string
}

// ToolCallPayload holds the data stored on a tool_call or tool_output
// [Turn]. CallID pairs a call with its output so [Conversation.Trim] and
// [Conversation.DropToolDetails] can keep them in sync.
type ToolCallPayload struct {
	CallID    string
	Name      string
	Arguments string
	Output    string
	Error     string
}

// Turn is one entry in a [Conversation]. Exactly one of Text or Tool is
// populated, selected by Role.
type Turn struct {
	Role      Role
	Text      string
	Tool      *ToolCallPayload
	Tier      Tier
	CreatedAt time.Time
}

// TokenUsage is the most recent token cost reported by the provider for a
// conversation. It is a snapshot of the last request, not a running total.
type TokenUsage struct {
	Input     int
	Cached    int
	Output    int
	Reasoning int
	Total     int
}

// RoleUsage is an approximate breakdown of the last request's non-cached
// input tokens across logical roles, computed from byte proportions. The
// values are estimates and should be treated as observability hints, not
// billing data.
type RoleUsage struct {
	Instructions int
	Tools        int
	User         int
	Assistant    int
	ToolIO       int
}

// AutoShrinkConfig drives [Conversation.maybeAutoShrink]. When
// ContextWindow is zero, auto shrinking is disabled. Threshold is a
// fraction of the window (for example 0.85). KeepToolExchanges bounds how
// many recent tool_output bodies are preserved verbatim. KeepTurns bounds
// how many trailing turns survive a trim pass.
type AutoShrinkConfig struct {
	ContextWindow     int
	Threshold         float64
	KeepToolExchanges int
	KeepTurns         int
}

// Summarizer collapses an older slice of turns into a single compact
// summary string. Implementations are typically backed by an LLM call but
// can also be deterministic (for example, joining titles for tests).
//
// Summarize must be safe to call from inside [Client.Send] - in
// particular, it must not call [Client.Send] on the same conversation it
// is summarising, or it will recurse.
type Summarizer interface {
	Summarize(ctx context.Context, turns []Turn) (string, error)
}

// SummarizerFunc adapts a plain function into a [Summarizer].
type SummarizerFunc func(ctx context.Context, turns []Turn) (string, error)

// Summarize implements [Summarizer].
func (f SummarizerFunc) Summarize(ctx context.Context, turns []Turn) (string, error) {
	return f(ctx, turns)
}

// Conversation is the ordered state of a single chat session.
//
// The zero value is not usable; construct one with [NewConversation].
// Conversation is not safe for concurrent use.
type Conversation struct {
	instructions string
	tools        []ToolSpec
	turns        []Turn
	usage        TokenUsage
	autoShrink   AutoShrinkConfig
	summarizer   Summarizer
}

// NewConversation creates an empty [Conversation] anchored on instructions
// and tools. Both values form the cache-stable prefix and are treated as
// immutable for the life of the conversation. A defensive copy of tools is
// made so later mutations by the caller do not disturb the cache key.
func NewConversation(instructions string, tools []ToolSpec) *Conversation {
	return &Conversation{
		instructions: instructions,
		tools:        append([]ToolSpec(nil), tools...),
		autoShrink: AutoShrinkConfig{
			Threshold:         0.85,
			KeepToolExchanges: 2,
			KeepTurns:         10,
		},
	}
}

// Instructions returns the system prompt bound at construction time.
func (c *Conversation) Instructions() string { return c.instructions }

// Tools returns a defensive copy of the advertised tool schema.
func (c *Conversation) Tools() []ToolSpec { return append([]ToolSpec(nil), c.tools...) }

// Turns returns a defensive copy of the recorded turns in insertion order.
func (c *Conversation) Turns() []Turn {
	out := make([]Turn, len(c.turns))
	copy(out, c.turns)
	return out
}

// Len returns the number of turns currently stored.
func (c *Conversation) Len() int { return len(c.turns) }

// Usage returns the token usage reported for the most recent request.
func (c *Conversation) Usage() TokenUsage { return c.usage }

// SetAutoShrink replaces the auto-shrink policy. Passing a zero-valued
// [AutoShrinkConfig] effectively disables auto shrinking.
func (c *Conversation) SetAutoShrink(cfg AutoShrinkConfig) { c.autoShrink = cfg }

// SetSummarizer registers a [Summarizer] used by [Conversation.Compress]
// and the auto-shrink pass to collapse old history into a summary turn.
// Passing nil disables summarisation; the auto-shrink pass then falls
// back to dropping old turns via [Conversation.Trim].
func (c *Conversation) SetSummarizer(s Summarizer) { c.summarizer = s }

// conversationJSON is the wire format used by [Conversation.MarshalJSON]
// and [Conversation.UnmarshalJSON]. Only the fields that form the
// persistent state of a conversation are encoded; runtime-only fields
// (summarizer, auto-shrink callbacks) are intentionally omitted.
type conversationJSON struct {
	Instructions string           `json:"instructions"`
	Tools        []ToolSpec       `json:"tools,omitempty"`
	Turns        []Turn           `json:"turns,omitempty"`
	Usage        TokenUsage       `json:"usage"`
	AutoShrink   AutoShrinkConfig `json:"auto_shrink"`
}

// MarshalJSON serialises the persistent state of the conversation. The
// registered [Summarizer] is not persisted because it is typically backed
// by an LLM client that should be rebuilt on restore.
func (c *Conversation) MarshalJSON() ([]byte, error) {
	return json.Marshal(conversationJSON{
		Instructions: c.instructions,
		Tools:        c.tools,
		Turns:        c.turns,
		Usage:        c.usage,
		AutoShrink:   c.autoShrink,
	})
}

// UnmarshalJSON restores a conversation previously produced by
// [Conversation.MarshalJSON]. Defensive copies of slices are stored so
// later caller mutations do not disturb the restored state.
func (c *Conversation) UnmarshalJSON(data []byte) error {
	var raw conversationJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.instructions = raw.Instructions
	c.tools = append([]ToolSpec(nil), raw.Tools...)
	c.turns = append([]Turn(nil), raw.Turns...)
	c.usage = raw.Usage
	c.autoShrink = raw.AutoShrink
	c.summarizer = nil
	return nil
}

// AppendUser records a user message.
func (c *Conversation) AppendUser(text string) {
	c.turns = append(c.turns, Turn{
		Role:      RoleUser,
		Text:      text,
		Tier:      TierVolatile,
		CreatedAt: time.Now(),
	})
}

// AppendAssistantText records an assistant text reply.
func (c *Conversation) AppendAssistantText(text string) {
	c.turns = append(c.turns, Turn{
		Role:      RoleAssistant,
		Text:      text,
		Tier:      TierStableHistory,
		CreatedAt: time.Now(),
	})
}

// AppendToolCall records a function call requested by the model.
func (c *Conversation) AppendToolCall(call ToolCall) {
	c.turns = append(c.turns, Turn{
		Role:      RoleToolCall,
		Tier:      TierToolIO,
		CreatedAt: time.Now(),
		Tool: &ToolCallPayload{
			CallID:    call.CallID,
			Name:      call.Name,
			Arguments: call.Arguments,
		},
	})
}

// AppendToolOutput records the output produced for a previous tool call.
// callID must match the CallID of a preceding tool_call turn. If execErr is
// non-nil its message is stored alongside the (possibly partial) output.
func (c *Conversation) AppendToolOutput(callID, output string, execErr error) {
	p := &ToolCallPayload{CallID: callID, Output: output}
	if execErr != nil {
		p.Error = execErr.Error()
	}
	c.turns = append(c.turns, Turn{
		Role:      RoleToolOutput,
		Tier:      TierToolIO,
		Tool:      p,
		CreatedAt: time.Now(),
	})
}

// Trim drops the oldest turns so that at most keepLastTurns remain. Tool
// call/output pairs are kept together: if the cut point would strand a
// tool_output without its preceding tool_call, the cut is nudged backward
// so the pair survives. keepLastTurns values <= 0 are treated as 0.
// The number of dropped turns is returned.
func (c *Conversation) Trim(keepLastTurns int) int {
	if keepLastTurns < 0 {
		keepLastTurns = 0
	}
	if len(c.turns) <= keepLastTurns {
		return 0
	}
	cut := len(c.turns) - keepLastTurns
	// Back up past any orphaned tool_output so its tool_call is kept too.
	for cut > 0 && cut < len(c.turns) && c.turns[cut].Role == RoleToolOutput {
		cut--
	}
	dropped := cut
	c.turns = append([]Turn(nil), c.turns[cut:]...)
	return dropped
}

// DropToolDetails replaces the Output body of older tool_output turns with
// a short placeholder, preserving the last keepLastN outputs verbatim. The
// structural turn is kept so tool_call/tool_output pairing remains intact.
// The number of outputs truncated is returned.
func (c *Conversation) DropToolDetails(keepLastN int) int {
	if keepLastN < 0 {
		keepLastN = 0
	}
	truncated := 0
	seen := 0
	for i := len(c.turns) - 1; i >= 0; i-- {
		t := &c.turns[i]
		if t.Role != RoleToolOutput || t.Tool == nil {
			continue
		}
		seen++
		if seen <= keepLastN {
			continue
		}
		if t.Tool.Output == "" {
			continue
		}
		orig := len(t.Tool.Output)
		t.Tool.Output = summarizeTruncated(orig)
		truncated++
	}
	return truncated
}

// ReplaceRange swaps turns[from:to] with replacement. It is the escape hatch
// for advanced edits (for example, replacing a run of stale turns with a
// summary turn). Invalid ranges are clamped to the slice bounds.
func (c *Conversation) ReplaceRange(from, to int, replacement []Turn) {
	if from < 0 {
		from = 0
	}
	if to > len(c.turns) {
		to = len(c.turns)
	}
	if from > to {
		from = to
	}
	merged := make([]Turn, 0, len(c.turns)-(to-from)+len(replacement))
	merged = append(merged, c.turns[:from]...)
	merged = append(merged, replacement...)
	merged = append(merged, c.turns[to:]...)
	c.turns = merged
}

// recordUsage stores the latest provider-reported usage and triggers an
// auto-shrink pass if the policy says so. It is called by [Client.Send].
// The returned error is non-nil only when a registered [Summarizer]
// failed; in that case the conversation has already been shrunk by
// [Conversation.Trim] as a fallback so the next request still fits.
func (c *Conversation) recordUsage(ctx context.Context, u TokenUsage) error {
	c.usage = u
	return c.maybeAutoShrink(ctx)
}

// maybeAutoShrink applies tier-ordered shrinking when the last request's
// input tokens exceed the configured threshold. It runs at most one pass
// per recorded usage sample to avoid repeatedly rewriting history between
// turns. The tier order is:
//
//  1. T2 - truncate old tool_output bodies (DropToolDetails);
//  2. T1.5 - if a [Summarizer] is registered, collapse the oldest turns
//     into a single summary turn via [Conversation.Compress];
//  3. T1 - otherwise drop the oldest turns beyond KeepTurns (Trim).
//
// When the summariser fails, maybeAutoShrink falls back to Trim so the
// next request still fits, and surfaces the original summariser error.
func (c *Conversation) maybeAutoShrink(ctx context.Context) error {
	cfg := c.autoShrink
	if cfg.ContextWindow <= 0 || cfg.Threshold <= 0 {
		return nil
	}
	limit := int(float64(cfg.ContextWindow) * cfg.Threshold)
	if limit <= 0 || c.usage.Input < limit {
		return nil
	}
	c.DropToolDetails(cfg.KeepToolExchanges)
	if c.summarizer != nil && len(c.turns) > cfg.KeepTurns {
		upto := len(c.turns) - cfg.KeepTurns
		if _, err := c.Compress(ctx, c.summarizer, upto); err != nil {
			c.Trim(cfg.KeepTurns)
			return err
		}
		return nil
	}
	c.Trim(cfg.KeepTurns)
	return nil
}

// summaryPrefix is prepended to the text of a summary turn produced by
// [Conversation.Compress]. It is deliberately short and deterministic so
// downstream prompt caches can treat the summary block as cacheable
// content once written.
const summaryPrefix = "Conversation summary (compressed history):\n"

// Compress collapses turns[0:uptoTurn] into a single assistant turn whose
// text is produced by summarizer. uptoTurn is nudged backward when it
// would split a tool_call/tool_output pair, preserving call/output
// adjacency. Compress is a no-op when summarizer is nil, when uptoTurn
// is <= 0, or when the adjusted cut would discard nothing. The number of
// turns replaced is returned alongside any summariser error; on error the
// conversation is left untouched.
func (c *Conversation) Compress(ctx context.Context, summarizer Summarizer, uptoTurn int) (int, error) {
	if summarizer == nil || uptoTurn <= 0 {
		return 0, nil
	}
	if uptoTurn > len(c.turns) {
		uptoTurn = len(c.turns)
	}
	for uptoTurn > 0 && uptoTurn < len(c.turns) && c.turns[uptoTurn].Role == RoleToolOutput {
		uptoTurn--
	}
	if uptoTurn <= 0 {
		return 0, nil
	}
	snapshot := append([]Turn(nil), c.turns[:uptoTurn]...)
	summary, err := summarizer.Summarize(ctx, snapshot)
	if err != nil {
		return 0, err
	}
	summaryTurn := Turn{
		Role:      RoleAssistant,
		Text:      summaryPrefix + summary,
		Tier:      TierStableHistory,
		CreatedAt: time.Now(),
	}
	c.ReplaceRange(0, uptoTurn, []Turn{summaryTurn})
	return uptoTurn, nil
}

// UsageByRole returns an approximate per-role breakdown of the last
// request's non-cached input tokens based on byte proportion of the
// serialized content. The sum of the fields equals
// max(0, Usage().Input - Usage().Cached).
func (c *Conversation) UsageByRole() RoleUsage {
	budget := c.usage.Input - c.usage.Cached
	if budget < 0 {
		budget = 0
	}
	if budget == 0 {
		return RoleUsage{}
	}

	instrBytes := len(c.instructions)
	toolBytes := 0
	for _, t := range c.tools {
		toolBytes += len(t.Name) + len(t.Description)
	}
	var userBytes, assistantBytes, toolIOBytes int
	for _, t := range c.turns {
		switch t.Role {
		case RoleUser:
			userBytes += len(t.Text)
		case RoleAssistant:
			assistantBytes += len(t.Text)
		case RoleToolCall:
			if t.Tool != nil {
				toolIOBytes += len(t.Tool.Name) + len(t.Tool.Arguments)
			}
		case RoleToolOutput:
			if t.Tool != nil {
				toolIOBytes += len(t.Tool.Output) + len(t.Tool.Error)
			}
		}
	}
	total := instrBytes + toolBytes + userBytes + assistantBytes + toolIOBytes
	if total == 0 {
		return RoleUsage{}
	}
	alloc := func(bytes int) int { return budget * bytes / total }
	return RoleUsage{
		Instructions: alloc(instrBytes),
		Tools:        alloc(toolBytes),
		User:         alloc(userBytes),
		Assistant:    alloc(assistantBytes),
		ToolIO:       alloc(toolIOBytes),
	}
}

// summarizeTruncated renders the placeholder body substituted for evicted
// tool outputs. It is deliberately short and deterministic so it does not
// perturb downstream prompt caches.
func summarizeTruncated(originalBytes int) string {
	return "[tool output truncated; " + strconv.Itoa(originalBytes) + " bytes]"
}
