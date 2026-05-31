package llm

import (
	"encoding/json"
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
	// RoleReasoning marks a trace of the model's chain of thought
	// (summary and/or public reasoning text) emitted by
	// reasoning-capable providers. It is captured for observability
	// and, when the turn carries a provider-opaque [Turn.Raw] payload
	// written by a Client with [ClientConfig.ReplayReasoning] enabled,
	// is replayed to the model on subsequent requests in the same
	// open tool-calling cycle to preserve reasoning continuity.
	RoleReasoning Role = "reasoning"
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

// Turn is one entry in a [Conversation]. Exactly one of Text or Tool is
// populated, selected by Role.
//
// Raw is an optional provider-opaque payload attached to a reasoning
// turn. It carries the JSON of an OpenAI Responses API
// ResponseReasoningItem captured when [ClientConfig.ReplayReasoning]
// is enabled so the reasoning item (including its id and
// encrypted_content) can be replayed on subsequent requests to
// preserve tool-calling continuity on reasoning models. Upper layers
// must not parse or mutate it.
type Turn struct {
	Role      Role
	Text      string
	Tool      *ToolCallPayload
	Tier      Tier
	CreatedAt time.Time
	Raw       json.RawMessage `json:"Raw,omitempty"`
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
