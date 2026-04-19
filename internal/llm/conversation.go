package llm

import (
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
func (c *Conversation) recordUsage(u TokenUsage) {
	c.usage = u
	c.maybeAutoShrink()
}

// maybeAutoShrink applies tier-ordered trimming when the last request's
// input tokens exceed the configured threshold. It runs at most one pass
// per recorded usage sample to avoid repeatedly rewriting history between
// turns. The tier order is:
//
//  1. T2 - truncate old tool_output bodies (DropToolDetails);
//  2. T1 - drop the oldest turns beyond KeepTurns (Trim).
//
// Phase B will insert a summarisation step between these, replacing the
// dropped prefix with a compact summary turn.
func (c *Conversation) maybeAutoShrink() {
	cfg := c.autoShrink
	if cfg.ContextWindow <= 0 || cfg.Threshold <= 0 {
		return
	}
	limit := int(float64(cfg.ContextWindow) * cfg.Threshold)
	if limit <= 0 || c.usage.Input < limit {
		return
	}
	c.DropToolDetails(cfg.KeepToolExchanges)
	c.Trim(cfg.KeepTurns)
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
