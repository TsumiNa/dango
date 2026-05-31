package llm

import (
	"encoding/json"
	"testing"
)

func TestEventJSONRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
	}{
		{"init", Event{Seq: 1, Kind: EventInit, Instructions: "sys", Tools: []ToolSpec{{Name: "t"}}}},
		{"user", Event{Seq: 2, Kind: EventAppendUser, Turn: &Turn{Role: RoleUser, Text: "hi"}}},
		{"tool_call", Event{Seq: 3, Kind: EventAppendToolCall, Turn: &Turn{
			Role: RoleToolCall,
			Tool: &ToolCallPayload{CallID: "c", Name: "t", Arguments: "{}"},
		}}},
		{"trim", Event{Seq: 4, Kind: EventTrim, KeepLast: 5}},
		{"replace_range", Event{Seq: 5, Kind: EventReplaceRange, From: 0, To: 3, Replacement: []Turn{{Role: RoleAssistant, Text: "sum"}}}},
		{"usage", Event{Seq: 6, Kind: EventRecordUsage, Usage: &TokenUsage{Input: 10, Output: 2, Total: 12}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.ev)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var back Event
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if back.Kind != tc.ev.Kind || back.Seq != tc.ev.Seq {
				t.Errorf("roundtrip mismatch: got %+v want %+v", back, tc.ev)
			}
		})
	}
}

func TestEventApplyAppendsTurn(t *testing.T) {
	c := mustNewConversation(t, nil, "sys", nil)
	ev := &Event{Kind: EventAppendUser, Turn: &Turn{Role: RoleUser, Text: "hi"}}
	if err := ev.apply(c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Len() != 1 || c.Turns()[0].Text != "hi" {
		t.Errorf("turns = %+v", c.Turns())
	}
}

func TestEventApplyAppendRequiresTurn(t *testing.T) {
	c := mustNewConversation(t, nil, "sys", nil)
	ev := &Event{Kind: EventAppendUser}
	if err := ev.apply(c); err == nil {
		t.Error("apply accepted append event without Turn")
	}
}

func TestEventApplyInitSetsAnchor(t *testing.T) {
	c := mustNewConversation(t, nil, "", nil)
	ev := &Event{Kind: EventInit, Instructions: "sys", Tools: []ToolSpec{{Name: "t"}}}
	if err := ev.apply(c); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Instructions() != "sys" {
		t.Errorf("Instructions = %q", c.Instructions())
	}
	if got := c.Tools(); len(got) != 1 || got[0].Name != "t" {
		t.Errorf("Tools = %+v", got)
	}
}

func TestEventApplyUnknownKind(t *testing.T) {
	c := mustNewConversation(t, nil, "", nil)
	ev := &Event{Kind: "bogus"}
	if err := ev.apply(c); err == nil {
		t.Error("apply accepted unknown kind")
	}
}
