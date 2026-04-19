package llm

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventKind tags the kind of state change an [Event] records. The tag
// is the JSON discriminator stored on disk and drives both replay and
// per-kind validation.
type EventKind string

// Event kinds. Every state-mutating method on [Conversation] corresponds
// to exactly one kind so a recorded log can be replayed deterministically
// without re-running side effects (for example, the original
// [Summarizer] is not invoked during replay; its output is captured in
// the [EventReplaceRange] event that [Conversation.Compress] emitted).
const (
	// EventInit anchors a session by recording the immutable
	// instructions and tool schema. It must be the first event in
	// every log; subsequent appends without a prior init are
	// rejected by the store.
	EventInit EventKind = "init"

	// EventAppendUser, EventAppendAssistant, EventAppendReasoning,
	// EventAppendToolCall, and EventAppendToolOutput record one
	// appended [Turn] each.
	EventAppendUser       EventKind = "append_user"
	EventAppendAssistant  EventKind = "append_assistant"
	EventAppendReasoning  EventKind = "append_reasoning"
	EventAppendToolCall   EventKind = "append_tool_call"
	EventAppendToolOutput EventKind = "append_tool_output"

	// EventTrim records the result of [Conversation.Trim] as the
	// number of trailing turns kept (KeepLast).
	EventTrim EventKind = "trim"

	// EventDropToolDetails records the result of
	// [Conversation.DropToolDetails] as the number of recent tool
	// outputs preserved verbatim (KeepLast).
	EventDropToolDetails EventKind = "drop_tool_details"

	// EventReplaceRange records the result of
	// [Conversation.ReplaceRange] - and, by extension,
	// [Conversation.Compress], which encodes the produced summary
	// turn as the Replacement so replay does not re-run the
	// summariser.
	EventReplaceRange EventKind = "replace_range"

	// EventRecordUsage records the most recent provider-reported
	// token usage. Replay restores Usage but does not re-run the
	// auto-shrink pass; any shrinking done at original record time
	// was already captured as its own EventTrim, EventReplaceRange,
	// or EventDropToolDetails event.
	EventRecordUsage EventKind = "record_usage"
)

// Event is a single append-only record in a session log. Seq is assigned
// monotonically by the [SessionStore] starting at 1 and gaps are not
// permitted. Only the fields named by Kind are meaningful; the rest are
// emitted as JSON omitempty so logs stay compact.
type Event struct {
	Seq       int64     `json:"seq"`
	Kind      EventKind `json:"kind"`
	Timestamp time.Time `json:"ts"`

	// Init payload.
	Instructions string     `json:"instructions,omitempty"`
	Tools        []ToolSpec `json:"tools,omitempty"`

	// Append* payload: the appended turn.
	Turn *Turn `json:"turn,omitempty"`

	// Trim and DropToolDetails payload.
	KeepLast int `json:"keep_last,omitempty"`

	// ReplaceRange payload. From defaults to 0 (start of log) when
	// omitted, To defaults to 0; readers identify the event by Kind
	// and never inspect these on other kinds.
	From        int    `json:"from,omitempty"`
	To          int    `json:"to,omitempty"`
	Replacement []Turn `json:"replacement,omitempty"`

	// RecordUsage payload.
	Usage *TokenUsage `json:"usage,omitempty"`
}

// apply mutates conv to reflect ev. It is the inverse of the emit
// performed by every state-changing [Conversation] method and is shared
// by the replay path so a single source of truth defines what each
// EventKind means. It does not emit further events; the caller is
// responsible for setting Conversation.replaying around any apply
// sequence that must not feed back into the store.
func (ev *Event) apply(c *Conversation) error {
	switch ev.Kind {
	case EventInit:
		c.instructions = ev.Instructions
		c.toolSpecs = append([]ToolSpec(nil), ev.Tools...)
	case EventAppendUser, EventAppendAssistant,
		EventAppendReasoning, EventAppendToolCall, EventAppendToolOutput:
		if ev.Turn == nil {
			return fmt.Errorf("llm: event %s missing turn", ev.Kind)
		}
		c.turns = append(c.turns, *ev.Turn)
	case EventTrim:
		c.Trim(ev.KeepLast)
	case EventDropToolDetails:
		c.DropToolDetails(ev.KeepLast)
	case EventReplaceRange:
		c.ReplaceRange(ev.From, ev.To, ev.Replacement)
	case EventRecordUsage:
		if ev.Usage != nil {
			c.usage = *ev.Usage
		}
	default:
		return fmt.Errorf("llm: unknown event kind %q", ev.Kind)
	}
	return nil
}

// String returns the canonical JSON encoding of the event, useful for
// debug logging and test failure messages.
func (ev *Event) String() string {
	b, err := json.Marshal(ev)
	if err != nil {
		return fmt.Sprintf("Event{seq=%d kind=%s err=%v}", ev.Seq, ev.Kind, err)
	}
	return string(b)
}
