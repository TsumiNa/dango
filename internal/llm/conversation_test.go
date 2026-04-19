package llm

import (
	"errors"
	"strings"
	"testing"
)

func TestConversationAppendRoles(t *testing.T) {
	conv := NewConversation("sys", []ToolSpec{{Name: "echo"}})
	conv.AppendUser("hi")
	conv.AppendAssistantText("hello")
	conv.AppendToolCall(ToolCall{CallID: "c1", Name: "echo", Arguments: `{"msg":"x"}`})
	conv.AppendToolOutput("c1", "x", nil)

	turns := conv.Turns()
	if len(turns) != 4 {
		t.Fatalf("got %d turns, want 4", len(turns))
	}
	wantRoles := []Role{RoleUser, RoleAssistant, RoleToolCall, RoleToolOutput}
	for i, r := range wantRoles {
		if turns[i].Role != r {
			t.Errorf("turn[%d].Role = %q, want %q", i, turns[i].Role, r)
		}
	}
	if turns[2].Tool == nil || turns[2].Tool.CallID != "c1" {
		t.Errorf("tool_call CallID not preserved")
	}
	if turns[3].Tool == nil || turns[3].Tool.Output != "x" {
		t.Errorf("tool_output Output not preserved")
	}
}

func TestConversationTurnsReturnsDefensiveCopy(t *testing.T) {
	conv := NewConversation("", nil)
	conv.AppendUser("hi")
	turns := conv.Turns()
	turns[0].Text = "mutated"
	if conv.Turns()[0].Text != "hi" {
		t.Errorf("Turns() returned aliased slice; mutation leaked")
	}
}

func TestConversationTrimKeepsPairs(t *testing.T) {
	conv := NewConversation("", nil)
	conv.AppendUser("u1")
	conv.AppendAssistantText("a1")
	conv.AppendToolCall(ToolCall{CallID: "c1", Name: "t"})
	conv.AppendToolOutput("c1", "out1", nil)
	conv.AppendUser("u2")
	conv.AppendAssistantText("a2")

	// Naive keep=3 would strand tool_output at index 3; Trim must back up
	// and keep the tool_call as well, yielding 4 surviving turns.
	dropped := conv.Trim(3)
	turns := conv.Turns()
	if dropped != 2 {
		t.Errorf("Trim dropped %d, want 2 (after backing up for pair)", dropped)
	}
	if len(turns) != 4 {
		t.Fatalf("got %d turns, want 4 (pair preserved)", len(turns))
	}
	if turns[0].Role != RoleToolCall || turns[1].Role != RoleToolOutput {
		t.Errorf("tool_call/tool_output pair not kept adjacent")
	}
}

func TestConversationTrimNoOpWhenWithinLimit(t *testing.T) {
	conv := NewConversation("", nil)
	conv.AppendUser("u1")
	conv.AppendAssistantText("a1")
	if dropped := conv.Trim(10); dropped != 0 {
		t.Errorf("Trim dropped %d, want 0", dropped)
	}
	if conv.Len() != 2 {
		t.Errorf("Len() = %d, want 2", conv.Len())
	}
}

func TestConversationDropToolDetailsTruncatesOldOutputs(t *testing.T) {
	conv := NewConversation("", nil)
	for i := 0; i < 3; i++ {
		conv.AppendToolCall(ToolCall{CallID: "c", Name: "t"})
		conv.AppendToolOutput("c", strings.Repeat("x", 100), nil)
	}
	truncated := conv.DropToolDetails(1)
	if truncated != 2 {
		t.Errorf("truncated = %d, want 2", truncated)
	}
	turns := conv.Turns()
	// outputs at indices 1, 3, 5; only index 5 (last) retains full body.
	if turns[1].Tool.Output == strings.Repeat("x", 100) {
		t.Errorf("index 1 output not truncated")
	}
	if turns[3].Tool.Output == strings.Repeat("x", 100) {
		t.Errorf("index 3 output not truncated")
	}
	if turns[5].Tool.Output != strings.Repeat("x", 100) {
		t.Errorf("index 5 output unexpectedly truncated")
	}
	// CallID must survive so the pair stays valid.
	if turns[1].Tool.CallID != "c" {
		t.Errorf("CallID lost during truncation")
	}
}

func TestConversationReplaceRange(t *testing.T) {
	conv := NewConversation("", nil)
	conv.AppendUser("u1")
	conv.AppendAssistantText("a1")
	conv.AppendUser("u2")

	conv.ReplaceRange(0, 2, []Turn{{Role: RoleAssistant, Text: "summary"}})
	turns := conv.Turns()
	if len(turns) != 2 || turns[0].Text != "summary" || turns[1].Text != "u2" {
		t.Errorf("ReplaceRange mismatch: %+v", turns)
	}
}

func TestConversationAutoShrinkTriggersTierOrder(t *testing.T) {
	conv := NewConversation("sys", nil)
	conv.SetAutoShrink(AutoShrinkConfig{
		ContextWindow:     1000,
		Threshold:         0.5,
		KeepToolExchanges: 1,
		KeepTurns:         3,
	})
	// Build a history with many turns including several tool pairs.
	for i := 0; i < 4; i++ {
		conv.AppendUser("hello")
		conv.AppendToolCall(ToolCall{CallID: "c", Name: "t"})
		conv.AppendToolOutput("c", strings.Repeat("y", 100), nil)
		conv.AppendAssistantText("done")
	}
	// Simulate a response over the threshold.
	conv.recordUsage(TokenUsage{Input: 800})

	turns := conv.Turns()
	if conv.Len() > 4 {
		t.Errorf("Trim did not apply: Len() = %d", conv.Len())
	}
	// Among surviving tool_output turns only the last must retain full body.
	var fullOutputs int
	for _, tn := range turns {
		if tn.Role == RoleToolOutput && tn.Tool != nil && tn.Tool.Output == strings.Repeat("y", 100) {
			fullOutputs++
		}
	}
	if fullOutputs > 1 {
		t.Errorf("auto-shrink kept %d full tool outputs, want <=1", fullOutputs)
	}
}

func TestConversationAutoShrinkDisabledByDefault(t *testing.T) {
	conv := NewConversation("", nil)
	for i := 0; i < 20; i++ {
		conv.AppendUser("msg")
	}
	conv.recordUsage(TokenUsage{Input: 1_000_000})
	if conv.Len() != 20 {
		t.Errorf("unexpected trim without ContextWindow set: Len() = %d", conv.Len())
	}
}

func TestConversationAppendToolOutputRecordsError(t *testing.T) {
	conv := NewConversation("", nil)
	conv.AppendToolOutput("c1", "partial", errors.New("boom"))
	turn := conv.Turns()[0]
	if turn.Tool.Error != "boom" || turn.Tool.Output != "partial" {
		t.Errorf("error/output not recorded: %+v", turn.Tool)
	}
}

func TestConversationUsageByRoleSumsToNonCached(t *testing.T) {
	conv := NewConversation("instr", []ToolSpec{{Name: "echo", Description: "e"}})
	conv.AppendUser("hello world")
	conv.AppendAssistantText("hi")
	conv.recordUsage(TokenUsage{Input: 120, Cached: 20})

	u := conv.UsageByRole()
	total := u.Instructions + u.Tools + u.User + u.Assistant + u.ToolIO
	// Integer division may under-attribute by at most (number of buckets).
	if total > 100 || total < 100-5 {
		t.Errorf("per-role sum = %d, want close to 100 (Input-Cached)", total)
	}
	if u.User == 0 || u.Instructions == 0 {
		t.Errorf("expected non-zero User and Instructions buckets: %+v", u)
	}
}
