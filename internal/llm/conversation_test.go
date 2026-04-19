package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestConversationAppendRoles(t *testing.T) {
	conv := NewConversation(nil, "sys", []ToolSpec{{Name: "echo"}})
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
	conv := NewConversation(nil, "", nil)
	conv.AppendUser("hi")
	turns := conv.Turns()
	turns[0].Text = "mutated"
	if conv.Turns()[0].Text != "hi" {
		t.Errorf("Turns() returned aliased slice; mutation leaked")
	}
}

func TestConversationTrimKeepsPairs(t *testing.T) {
	conv := NewConversation(nil, "", nil)
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

// TestConversationTrimKeepsPairWithInterleavedReasoning covers the case
// where a RoleReasoning turn lands between a tool_call and its tool_output
// (for example because a model emitted [function_call, reasoning] inside
// one response). Without rewinding past reasoning, Trim would stop on the
// reasoning turn and strand the tool_output from its tool_call.
func TestConversationTrimKeepsPairWithInterleavedReasoning(t *testing.T) {
	conv := NewConversation(nil, "", nil)
	conv.AppendUser("u1")
	conv.AppendAssistantText("a1")
	conv.AppendToolCall(ToolCall{CallID: "c1", Name: "t"})
	conv.AppendReasoning("midway thought", nil)
	conv.AppendToolOutput("c1", "out1", nil)
	conv.AppendUser("u2")
	conv.AppendAssistantText("a2")

	// keep=3 puts the cut on tool_output; backup must rewind past both
	// the reasoning turn and the tool_call so the pair survives.
	conv.Trim(3)

	var calls, outputs int
	for _, tr := range conv.Turns() {
		switch tr.Role {
		case RoleToolCall:
			calls++
		case RoleToolOutput:
			outputs++
		}
	}
	if outputs > calls {
		t.Errorf("Trim orphaned tool_output: %d tool_call vs %d tool_output; turns=%+v",
			calls, outputs, conv.Turns())
	}

	// The replay path must still produce a valid input sequence.
	if in := buildResponseInput(conv.Turns()); len(in) == 0 {
		t.Fatal("buildResponseInput returned empty input after Trim")
	}
}

func TestConversationTrimNoOpWhenWithinLimit(t *testing.T) {
	conv := NewConversation(nil, "", nil)
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
	conv := NewConversation(nil, "", nil)
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
	conv := NewConversation(nil, "", nil)
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
	conv := NewConversation(nil, "sys", nil)
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
	if err := conv.recordUsage(t.Context(), TokenUsage{Input: 800}); err != nil {
		t.Fatalf("recordUsage: %v", err)
	}

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
	conv := NewConversation(nil, "", nil)
	for i := 0; i < 20; i++ {
		conv.AppendUser("msg")
	}
	if err := conv.recordUsage(t.Context(), TokenUsage{Input: 1_000_000}); err != nil {
		t.Fatalf("recordUsage: %v", err)
	}
	if conv.Len() != 20 {
		t.Errorf("unexpected trim without ContextWindow set: Len() = %d", conv.Len())
	}
}

func TestConversationAppendToolOutputRecordsError(t *testing.T) {
	conv := NewConversation(nil, "", nil)
	conv.AppendToolOutput("c1", "partial", errors.New("boom"))
	turn := conv.Turns()[0]
	if turn.Tool.Error != "boom" || turn.Tool.Output != "partial" {
		t.Errorf("error/output not recorded: %+v", turn.Tool)
	}
}

func TestConversationUsageByRoleSumsToNonCached(t *testing.T) {
	conv := NewConversation(nil, "instr", []ToolSpec{{Name: "echo", Description: "e"}})
	conv.AppendUser("hello world")
	conv.AppendAssistantText("hi")
	if err := conv.recordUsage(t.Context(), TokenUsage{Input: 120, Cached: 20}); err != nil {
		t.Fatalf("recordUsage: %v", err)
	}

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

func TestConversationCompressReplacesPrefixWithSummary(t *testing.T) {
	conv := NewConversation(nil, "", nil)
	conv.AppendUser("u1")
	conv.AppendAssistantText("a1")
	conv.AppendUser("u2")
	conv.AppendAssistantText("a2")

	var got []Turn
	sum := SummarizerFunc(func(_ context.Context, turns []Turn) (string, error) {
		got = turns
		return "older history happened", nil
	})

	replaced, err := conv.Compress(t.Context(), sum, 3)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if replaced != 3 {
		t.Errorf("replaced = %d, want 3", replaced)
	}
	if len(got) != 3 || got[0].Text != "u1" || got[2].Text != "u2" {
		t.Errorf("summarizer did not see expected snapshot: %+v", got)
	}
	turns := conv.Turns()
	if len(turns) != 2 {
		t.Fatalf("Len after compress = %d, want 2", len(turns))
	}
	if turns[0].Role != RoleAssistant || !strings.Contains(turns[0].Text, "older history happened") {
		t.Errorf("summary turn malformed: %+v", turns[0])
	}
	if turns[1].Text != "a2" {
		t.Errorf("trailing turn lost: %+v", turns[1])
	}
}

func TestConversationCompressRespectsToolPair(t *testing.T) {
	conv := NewConversation(nil, "", nil)
	conv.AppendUser("u1")
	conv.AppendToolCall(ToolCall{CallID: "c", Name: "t"})
	conv.AppendToolOutput("c", "out", nil)
	conv.AppendAssistantText("a1")

	sum := SummarizerFunc(func(_ context.Context, turns []Turn) (string, error) {
		// Cut at index 2 would split the tool pair; Compress must back up
		// to 1 so the summarizer only sees the user turn.
		if len(turns) != 1 {
			t.Errorf("summarizer received %d turns, want 1", len(turns))
		}
		return "s", nil
	})
	replaced, err := conv.Compress(t.Context(), sum, 2)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if replaced != 1 {
		t.Errorf("replaced = %d, want 1 (cut backed up before tool pair)", replaced)
	}
	turns := conv.Turns()
	// Surviving tool_call/output pair must remain adjacent.
	if turns[1].Role != RoleToolCall || turns[2].Role != RoleToolOutput {
		t.Errorf("tool pair broken after compress: %+v", turns)
	}
}

// TestConversationCompressRewindsPastReasoning covers the case where
// the Compress cut lands on a tool_output preceded by
// [tool_call, reasoning, tool_output]. Without rewinding past reasoning
// as well, Compress would back up only one step, keep the reasoning and
// tool_output in the tail, and discard the tool_call - leaving the next
// Send with a function_call_output that has no matching function_call.
func TestConversationCompressRewindsPastReasoning(t *testing.T) {
	conv := NewConversation(nil, "", nil)
	conv.AppendUser("u1")
	conv.AppendToolCall(ToolCall{CallID: "c", Name: "t"})
	conv.AppendReasoning("midway thought", nil)
	conv.AppendToolOutput("c", "out", nil)
	conv.AppendAssistantText("a1")

	sum := SummarizerFunc(func(_ context.Context, turns []Turn) (string, error) {
		// Cut at index 3 initially; rewind must pull it back to 1
		// (before the tool_call) so the pair stays together.
		if len(turns) != 1 {
			t.Errorf("summarizer received %d turns, want 1", len(turns))
		}
		return "s", nil
	})
	if _, err := conv.Compress(t.Context(), sum, 3); err != nil {
		t.Fatalf("Compress: %v", err)
	}

	var calls, outputs int
	for _, tr := range conv.Turns() {
		switch tr.Role {
		case RoleToolCall:
			calls++
		case RoleToolOutput:
			outputs++
		}
	}
	if outputs > calls {
		t.Errorf("Compress orphaned tool_output: %d tool_call vs %d tool_output; turns=%+v",
			calls, outputs, conv.Turns())
	}
}

func TestConversationCompressNoOpOnNilSummarizer(t *testing.T) {
	conv := NewConversation(nil, "", nil)
	conv.AppendUser("u1")
	conv.AppendUser("u2")
	replaced, err := conv.Compress(t.Context(), nil, 2)
	if err != nil || replaced != 0 {
		t.Errorf("nil summarizer: replaced=%d err=%v, want (0,nil)", replaced, err)
	}
	if conv.Len() != 2 {
		t.Errorf("conversation mutated despite nil summarizer")
	}
}

func TestConversationCompressReturnsSummarizerError(t *testing.T) {
	conv := NewConversation(nil, "", nil)
	conv.AppendUser("u1")
	conv.AppendUser("u2")
	bad := SummarizerFunc(func(_ context.Context, _ []Turn) (string, error) {
		return "", errors.New("bad")
	})
	if _, err := conv.Compress(t.Context(), bad, 2); err == nil {
		t.Fatal("expected error from summarizer")
	}
	if conv.Len() != 2 {
		t.Errorf("Compress mutated conversation on error: Len()=%d", conv.Len())
	}
}

func TestConversationAutoShrinkUsesSummarizerWhenSet(t *testing.T) {
	conv := NewConversation(nil, "sys", nil)
	conv.SetAutoShrink(AutoShrinkConfig{
		ContextWindow:     1000,
		Threshold:         0.5,
		KeepToolExchanges: 1,
		KeepTurns:         3,
	})
	called := 0
	conv.SetSummarizer(SummarizerFunc(func(_ context.Context, _ []Turn) (string, error) {
		called++
		return "summary", nil
	}))
	for i := 0; i < 8; i++ {
		conv.AppendUser("hello")
		conv.AppendAssistantText("hi")
	}
	if err := conv.recordUsage(t.Context(), TokenUsage{Input: 800}); err != nil {
		t.Fatalf("recordUsage: %v", err)
	}
	if called != 1 {
		t.Errorf("summarizer called %d times, want 1", called)
	}
	turns := conv.Turns()
	if len(turns) != 4 {
		t.Fatalf("Len after shrink = %d, want 4 (1 summary + KeepTurns)", len(turns))
	}
	if turns[0].Role != RoleAssistant || !strings.Contains(turns[0].Text, "summary") {
		t.Errorf("first turn after shrink not a summary turn: %+v", turns[0])
	}
}

func TestConversationAutoShrinkFallsBackOnSummarizerError(t *testing.T) {
	conv := NewConversation(nil, "", nil)
	conv.SetAutoShrink(AutoShrinkConfig{
		ContextWindow:     1000,
		Threshold:         0.5,
		KeepToolExchanges: 0,
		KeepTurns:         2,
	})
	conv.SetSummarizer(SummarizerFunc(func(_ context.Context, _ []Turn) (string, error) {
		return "", errors.New("nope")
	}))
	for i := 0; i < 6; i++ {
		conv.AppendUser("msg")
	}
	err := conv.recordUsage(t.Context(), TokenUsage{Input: 800})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("recordUsage err = %v, want summarizer error", err)
	}
	if conv.Len() != 2 {
		t.Errorf("fallback Trim did not run: Len()=%d", conv.Len())
	}
}
