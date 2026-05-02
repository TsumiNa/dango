package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
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

// ConversationConfig configures optional runtime behaviour for a
// [Conversation].
//
// A nil *ConversationConfig passed to [NewConversation] uses the default
// conversation settings. Non-zero MaxSteps overrides the request/tool-call
// loop bound, AutoShrink overrides the default shrinking policy when non-nil,
// Summarizer enables summary-based compression, and Stream configures compact
// model/tool progress events for outer orchestration layers. Stream output is
// observational and independent of session persistence.
type ConversationConfig struct {
	MaxSteps       int
	AutoShrink     *AutoShrinkConfig
	Summarizer     Summarizer
	Stream         *streampkg.Stream
	StreamSource   streampkg.Source
	StreamScope    streampkg.Scope
	StreamMetadata map[string]any
}

// Summarizer collapses an older slice of turns into a single compact
// summary string. Implementations are typically backed by an LLM call but
// can also be deterministic (for example, joining titles for tests).
//
// Summarize must be safe to call from inside [Conversation.Send] - in
// particular, it must not call [Conversation.Send] on the same conversation it
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
	client       *Client
	instructions string

	// tools holds the executable [Tool] implementations registered at
	// construction time. toolSpecs is the wire format derived from
	// tools (and used directly when a conversation is restored from
	// JSON or an event log, where no executables are available).
	// toolByName indexes tools by Name for fast dispatch during
	// [Conversation.Run].
	tools      []Tool
	toolSpecs  []ToolSpec
	toolByName map[string]Tool

	turns      []Turn
	usage      TokenUsage
	autoShrink AutoShrinkConfig
	summarizer Summarizer

	// maxSteps bounds the number of request/tool-call iterations
	// [Conversation.Run] will perform before giving up. Zero is
	// treated as [DefaultMaxSteps].
	maxSteps int

	// Stream output. When configured, model/tool progress is emitted as
	// compact stream events. Large generated content should be written as
	// artifacts and referenced from events rather than embedded raw.
	eventStream   *streampkg.Stream
	eventSource   streampkg.Source
	eventScope    streampkg.Scope
	eventMetadata map[string]any

	// Persistence. stores and sessionID are set by [Conversation.OpenSession];
	// when stores is empty all mutating methods are pure in-memory updates.
	// Session persistence records lifecycle state only; it is not a mirror of
	// the outward stream. replaying suppresses emission while applying a loaded
	// log so replay does not feed back into the stores. lastErr captures the
	// most recent emit failure for [Conversation.LastError].
	stores    []SessionStore
	sessionID string
	replaying bool
	lastErr   error
}

// DefaultMaxSteps bounds the number of request/tool-call iterations
// [Conversation.Run] performs before giving up. It protects against
// runaway loops where the model keeps requesting tool calls without
// ever producing a final message.
const DefaultMaxSteps = 20

// NewConversation creates an empty [Conversation] anchored on
// instructions and tools and bound to client. client is the transport
// used by [Conversation.Send] and [Conversation.Stream]; when nil the
// conversation is a pure-history object that supports local mutations
// and JSON round-trips but will return [ErrNoClient] from any method
// that issues an LLM request.
//
// Each [Tool] in tools is advertised to the model via its Name,
// Description, and Parameters, and is dispatched directly by
// [Conversation.Run] when the model emits a matching function call.
// Tool names must be unique; duplicates return an error because the ambiguity
// would silently shadow a tool at call time.
//
// Instructions and the derived tool schema form the cache-stable
// prefix and are treated as immutable for the life of the
// conversation. NewConversation builds a private copy of both so
// later mutations by the caller do not disturb the cache key. cfg may be nil
// to use the default max-step and auto-shrink settings.
func NewConversation(client *Client, instructions string, tools []Tool, cfg *ConversationConfig) (*Conversation, error) {
	specs := make([]ToolSpec, len(tools))
	byName := make(map[string]Tool, len(tools))
	for i, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("llm: NewConversation received a nil tool")
		}
		name := t.Name()
		if name == "" {
			return nil, fmt.Errorf("llm: NewConversation received a tool with empty name")
		}
		if _, dup := byName[name]; dup {
			return nil, fmt.Errorf("llm: NewConversation received duplicate tool %q", name)
		}
		specs[i] = ToolSpec{
			Name:        name,
			Description: t.Description(),
			Parameters:  t.Parameters(),
		}
		byName[name] = t
	}
	c := &Conversation{
		client:       client,
		instructions: instructions,
		tools:        append([]Tool(nil), tools...),
		toolSpecs:    specs,
		toolByName:   byName,
		maxSteps:     DefaultMaxSteps,
		autoShrink: AutoShrinkConfig{
			Threshold:         0.85,
			KeepToolExchanges: 2,
			KeepTurns:         10,
		},
	}
	if cfg != nil {
		if cfg.MaxSteps > 0 {
			c.maxSteps = cfg.MaxSteps
		}
		if cfg.AutoShrink != nil {
			c.autoShrink = *cfg.AutoShrink
		}
		if cfg.Summarizer != nil {
			c.summarizer = cfg.Summarizer
		}
		if cfg.Stream != nil {
			source := cfg.StreamSource
			if source.Layer == "" {
				source.Layer = "conversation"
			}
			c.eventStream = cfg.Stream
			c.eventSource = source
			c.eventScope = cfg.StreamScope
			c.eventMetadata = cloneConversationStreamMetadata(cfg.StreamMetadata)
		}
	}
	return c, nil
}

// Client returns the [Client] bound at construction time, or nil when
// the conversation was created without one.
func (c *Conversation) Client() *Client { return c.client }

// SetClient replaces the bound [Client]. It is primarily intended for
// conversations restored from JSON, whose client field is dropped by
// the persistence layer; callers must rebind a client before invoking
// [Conversation.Send] or [Conversation.Stream].
func (c *Conversation) SetClient(client *Client) { c.client = client }

// OpenSession binds c to one or more stores under sessionID and either replays
// the existing event log or seeds a fresh one with c's current instructions
// and tools.
//
// On a fresh session (no events on any store) OpenSession emits an
// [EventInit] carrying c's current instructions and tools so future
// loads can reconstruct the cache anchor.
//
// On an existing session every recorded event is applied to c in
// order, replacing c's current instructions/tool schema/turns/usage with
// the persisted values. The arguments passed to [NewConversation] are
// therefore ignored when resuming an existing session, though any tool
// name advertised by the persisted session must still be present in the
// initial registered tool set so dispatch succeeds. The persisted
// init event is authoritative.
//
// OpenSession must be called before any mutating method (AppendUser,
// AppendAssistantText, ...) so the recorded log is a complete record
// of c's state. Calling it on a conversation that already has turns
// returns an error rather than silently mixing transient and
// persisted history. When multiple stores are provided, the first store that
// contains sessionID supplies the replay log and subsequent mutations are
// appended to every store.
func (c *Conversation) OpenSession(ctx context.Context, sessionID string, stores ...SessionStore) error {
	if len(stores) == 0 {
		return fmt.Errorf("llm: OpenSession requires at least one store")
	}
	for i, store := range stores {
		if store == nil {
			return fmt.Errorf("llm: OpenSession store %d is nil", i)
		}
	}
	if len(c.stores) > 0 {
		return fmt.Errorf("llm: conversation is already bound to a session")
	}
	if len(c.turns) > 0 {
		return fmt.Errorf("llm: OpenSession requires a fresh conversation (has %d turns)", len(c.turns))
	}

	events, sourceIndex, err := loadSessionEvents(ctx, sessionID, stores)
	switch {
	case err == nil:
		c.replaying = true
		for i := range events {
			if applyErr := events[i].apply(c); applyErr != nil {
				c.replaying = false
				return fmt.Errorf("llm: replay event seq=%d kind=%s: %w",
					events[i].Seq, events[i].Kind, applyErr)
			}
			if events[i].Kind == EventInit {
				for _, ts := range events[i].Tools {
					if _, ok := c.toolByName[ts.Name]; !ok {
						c.replaying = false
						return fmt.Errorf("llm: session %q requires tool %q which is not registered", sessionID, ts.Name)
					}
				}
			}
		}
		c.replaying = false
		if err := mirrorSessionEvents(ctx, sessionID, stores, sourceIndex, events); err != nil {
			return err
		}
	case errors.Is(err, ErrSessionNotFound):
		init := &Event{
			Kind:         EventInit,
			Instructions: c.instructions,
			Tools:        append([]ToolSpec(nil), c.toolSpecs...),
		}
		for i, store := range stores {
			if _, appendErr := store.Append(ctx, sessionID, cloneEvent(init)); appendErr != nil {
				return fmt.Errorf("llm: seed session %q store %d: %w", sessionID, i, appendErr)
			}
		}
	default:
		return fmt.Errorf("llm: load session %q: %w", sessionID, err)
	}

	c.stores = append([]SessionStore(nil), stores...)
	c.sessionID = sessionID
	return nil
}

func loadSessionEvents(ctx context.Context, sessionID string, stores []SessionStore) ([]Event, int, error) {
	for i, store := range stores {
		events, err := store.Load(ctx, sessionID)
		if err == nil {
			return events, i, nil
		}
		if !errors.Is(err, ErrSessionNotFound) {
			return nil, -1, fmt.Errorf("llm: load session %q store %d (%T): %w", sessionID, i, store, err)
		}
	}
	return nil, -1, ErrSessionNotFound
}

func mirrorSessionEvents(ctx context.Context, sessionID string, stores []SessionStore, sourceIndex int, events []Event) error {
	for i, store := range stores {
		if i == sourceIndex {
			continue
		}
		if _, err := store.Load(ctx, sessionID); err == nil {
			continue
		} else if !errors.Is(err, ErrSessionNotFound) {
			return fmt.Errorf("llm: load mirrored session %q store %d: %w", sessionID, i, err)
		}
		for _, ev := range events {
			if _, err := store.Append(ctx, sessionID, cloneEvent(&ev)); err != nil {
				return fmt.Errorf("llm: mirror session %q store %d: %w", sessionID, i, err)
			}
		}
	}
	return nil
}

// SessionID returns the id this conversation is persisted under, or an
// empty string when no session is bound.
func (c *Conversation) SessionID() string { return c.sessionID }

// LastError returns the first persistence error (if any) encountered
// since the session was bound. A failed emit is latched because the event
// log is already divergent; it is not reset on subsequent successful emits.
// Callers that require strict persistence should check it after each
// batch of mutations.
func (c *Conversation) LastError() error { return c.lastErr }

// Truncate rolls back the bound session's log to toSeq, discarding
// every event with Seq > toSeq, and reloads c from the truncated log.
// Passing toSeq <= 0 leaves only the [EventInit] anchor (and an empty
// turn list). Truncate is a no-op error when no session is bound.
func (c *Conversation) Truncate(ctx context.Context, toSeq int64) error {
	if len(c.stores) == 0 {
		return fmt.Errorf("llm: Truncate requires an open session")
	}
	for i, store := range c.stores {
		if err := store.Truncate(ctx, c.sessionID, toSeq); err != nil {
			return fmt.Errorf("llm: truncate session %q store %d: %w", c.sessionID, i, err)
		}
	}
	events, err := c.stores[0].Load(ctx, c.sessionID)
	if err != nil {
		return err
	}
	c.instructions = ""
	c.toolSpecs = nil
	c.turns = nil
	c.usage = TokenUsage{}
	c.replaying = true
	defer func() { c.replaying = false }()
	for i := range events {
		if applyErr := events[i].apply(c); applyErr != nil {
			return fmt.Errorf("llm: replay event seq=%d kind=%s: %w",
				events[i].Seq, events[i].Kind, applyErr)
		}
	}
	return nil
}

// emit records ev to the bound session store, if any. Errors are
// retained on c so callers can observe them via [Conversation.LastError]
// without requiring every mutating method to return an error. The first
// emit failure is latched; it is not cleared by subsequent successful
// emits because the event log is already divergent. emit is a no-op
// when no store is bound or when c is replaying a loaded log.
func (c *Conversation) emit(ev *Event) {
	if len(c.stores) == 0 || c.replaying {
		return
	}
	for _, store := range c.stores {
		if _, err := store.Append(context.Background(), c.sessionID, cloneEvent(ev)); err != nil {
			if c.lastErr == nil {
				c.lastErr = err
			}
		}
	}
}

func cloneEvent(ev *Event) *Event {
	if ev == nil {
		return nil
	}
	out := *ev
	out.Tools = append([]ToolSpec(nil), ev.Tools...)
	if ev.Turn != nil {
		turn := cloneTurn(*ev.Turn)
		out.Turn = &turn
	}
	out.Replacement = cloneTurns(ev.Replacement)
	if ev.Usage != nil {
		usage := *ev.Usage
		out.Usage = &usage
	}
	return &out
}

func cloneTurns(turns []Turn) []Turn {
	if turns == nil {
		return nil
	}
	out := make([]Turn, len(turns))
	for i, turn := range turns {
		out[i] = cloneTurn(turn)
	}
	return out
}

func cloneTurn(turn Turn) Turn {
	out := turn
	if turn.Tool != nil {
		tool := *turn.Tool
		out.Tool = &tool
	}
	out.Raw = append(json.RawMessage(nil), turn.Raw...)
	return out
}

// Instructions returns the system prompt bound at construction time.
func (c *Conversation) Instructions() string { return c.instructions }

// Tools returns a defensive copy of the advertised tool schema.
func (c *Conversation) Tools() []ToolSpec { return append([]ToolSpec(nil), c.toolSpecs...) }

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
		Tools:        c.toolSpecs,
		Turns:        c.turns,
		Usage:        c.usage,
		AutoShrink:   c.autoShrink,
	})
}

// UnmarshalJSON restores a conversation previously produced by
// [Conversation.MarshalJSON]. The executable [Tool] set is not part of
// the wire format; restored conversations therefore have no
// [Conversation.Run] dispatch table until the caller passes the
// original tools through a fresh [NewConversation] + [Conversation.OpenSession]
// pair. Defensive copies of slices are stored so later caller
// mutations do not disturb the restored state.
func (c *Conversation) UnmarshalJSON(data []byte) error {
	var raw conversationJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.instructions = raw.Instructions
	c.tools = nil
	c.toolSpecs = append([]ToolSpec(nil), raw.Tools...)
	c.toolByName = nil
	c.turns = append([]Turn(nil), raw.Turns...)
	c.usage = raw.Usage
	c.autoShrink = raw.AutoShrink
	c.summarizer = nil
	return nil
}

// AppendUser records a user message.
func (c *Conversation) AppendUser(text string) {
	turn := Turn{
		Role:      RoleUser,
		Text:      text,
		Tier:      TierVolatile,
		CreatedAt: time.Now(),
	}
	c.turns = append(c.turns, turn)
	c.emit(&Event{Kind: EventAppendUser, Turn: &turn})
}

// AppendAssistantText records an assistant text reply.
func (c *Conversation) AppendAssistantText(text string) {
	turn := Turn{
		Role:      RoleAssistant,
		Text:      text,
		Tier:      TierStableHistory,
		CreatedAt: time.Now(),
	}
	c.turns = append(c.turns, turn)
	c.emit(&Event{Kind: EventAppendAssistant, Turn: &turn})
}

// AppendReasoning records a reasoning trace emitted by the model. text
// typically combines the provider-visible summary and any
// reasoning_text content and is stored for observability. raw is an
// optional provider-opaque payload (see [Turn.Raw]) that, when set,
// lets [Conversation.Send] replay the captured reasoning item (including
// its id and encrypted_content) on subsequent requests so
// tool-calling continuity is preserved. An empty text with a nil raw
// is ignored so providers that never emit reasoning items do not
// pollute the turn log.
func (c *Conversation) AppendReasoning(text string, raw json.RawMessage) {
	if text == "" && len(raw) == 0 {
		return
	}
	turn := Turn{
		Role:      RoleReasoning,
		Text:      text,
		Tier:      TierToolIO,
		CreatedAt: time.Now(),
		Raw:       raw,
	}
	c.turns = append(c.turns, turn)
	c.emit(&Event{Kind: EventAppendReasoning, Turn: &turn})
}

// AppendToolCall records a function call requested by the model.
func (c *Conversation) AppendToolCall(call ToolCall) {
	turn := Turn{
		Role:      RoleToolCall,
		Tier:      TierToolIO,
		CreatedAt: time.Now(),
		Tool: &ToolCallPayload{
			CallID:    call.CallID,
			Name:      call.Name,
			Arguments: call.Arguments,
		},
	}
	c.turns = append(c.turns, turn)
	c.emit(&Event{Kind: EventAppendToolCall, Turn: &turn})
}

// AppendToolOutput records the output produced for a previous tool call.
// callID must match the CallID of a preceding tool_call turn. If execErr is
// non-nil its message is stored alongside the (possibly partial) output.
func (c *Conversation) AppendToolOutput(callID, output string, execErr error) {
	p := &ToolCallPayload{CallID: callID, Output: output}
	if execErr != nil {
		p.Error = execErr.Error()
	}
	turn := Turn{
		Role:      RoleToolOutput,
		Tier:      TierToolIO,
		Tool:      p,
		CreatedAt: time.Now(),
	}
	c.turns = append(c.turns, turn)
	c.emit(&Event{Kind: EventAppendToolOutput, Turn: &turn})
	c.emitToolResult(context.Background(), callID, output, execErr)
}

// Trim drops the oldest turns so that at most keepLastTurns remain. Tool
// call/output pairs are kept together: if the cut point would strand a
// tool_output without its preceding tool_call, the cut is nudged backward
// so the pair survives. Reasoning turns between a tool_call and its
// tool_output are also rewound past so the pair is not broken by an
// intervening debug-only entry. keepLastTurns values <= 0 are treated as 0.
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
	// Reasoning turns are rewound past as well because they carry no
	// structural meaning on their own and may sit between a tool_call
	// and its tool_output.
	for cut > 0 && cut < len(c.turns) {
		r := c.turns[cut].Role
		if r != RoleToolOutput && r != RoleReasoning {
			break
		}
		cut--
	}
	dropped := cut
	c.turns = append([]Turn(nil), c.turns[cut:]...)
	c.emit(&Event{Kind: EventTrim, KeepLast: keepLastTurns})
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
	if truncated > 0 {
		c.emit(&Event{Kind: EventDropToolDetails, KeepLast: keepLastN})
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
	c.emit(&Event{
		Kind:        EventReplaceRange,
		From:        from,
		To:          to,
		Replacement: append([]Turn(nil), replacement...),
	})
}

// recordUsage stores the latest provider-reported usage and triggers an
// auto-shrink pass if the policy says so. It is called by [Conversation.Send].
// The returned error is non-nil only when a registered [Summarizer]
// failed; in that case the conversation has already been shrunk by
// [Conversation.Trim] as a fallback so the next request still fits.
func (c *Conversation) recordUsage(ctx context.Context, u TokenUsage) error {
	c.usage = u
	usageCopy := u
	c.emit(&Event{Kind: EventRecordUsage, Usage: &usageCopy})
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
	// Rewind past tool_output and reasoning so the summariser never
	// strands a tool_output from its tool_call. Reasoning carries no
	// structural meaning and is safe to fold into the summary with
	// its neighbouring tool_call.
	for uptoTurn > 0 && uptoTurn < len(c.turns) {
		r := c.turns[uptoTurn].Role
		if r != RoleToolOutput && r != RoleReasoning {
			break
		}
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
	for _, t := range c.toolSpecs {
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
