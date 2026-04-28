package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseResponse writes the given event lines as a single SSE payload
// and closes the response. The helper keeps the tests focused on
// what the Stream code does, not on SSE framing details.
func sseResponse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, e := range events {
		_, _ = w.Write([]byte(e))
		if !strings.HasSuffix(e, "\n\n") {
			_, _ = w.Write([]byte("\n\n"))
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// textDeltaEvent renders a response.output_text.delta SSE frame.
func textDeltaEvent(delta string) string {
	return fmt.Sprintf(
		"event: response.output_text.delta\n"+
			"data: {\"type\":\"response.output_text.delta\",\"delta\":%q,\"item_id\":\"m1\",\"output_index\":0,\"content_index\":0,\"sequence_number\":0}",
		delta,
	)
}

// reasoningDeltaEvent renders a reasoning delta SSE frame. The
// eventType argument lets tests cover both response.reasoning_text.delta
// and response.reasoning_summary_text.delta, which the Stream worker
// aggregates under the same ReasoningDelta field.
func reasoningDeltaEvent(eventType, delta string) string {
	return fmt.Sprintf(
		"event: %s\ndata: {\"type\":%q,\"delta\":%q,\"item_id\":\"r1\",\"output_index\":0,\"content_index\":0,\"sequence_number\":0}",
		eventType, eventType, delta,
	)
}

// completedEvent renders a response.completed SSE frame with the
// given assistant text and optional function_call.
func completedEvent(text, callName, callArgs string) string {
	outputs := fmt.Sprintf(
		`{"id":"m1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}`,
		text,
	)
	if callName != "" {
		outputs += fmt.Sprintf(
			`,{"id":"fc1","type":"function_call","status":"completed","call_id":"call_1","name":%q,"arguments":%q}`,
			callName, callArgs,
		)
	}
	data := fmt.Sprintf(
		`{"type":"response.completed","sequence_number":0,"response":{"id":"r1","object":"response","created_at":0,"model":"test-model","status":"completed","output":[%s],"parallel_tool_calls":false,"tool_choice":"auto","tools":[],"usage":{"input_tokens":3,"input_tokens_details":{"cached_tokens":0},"output_tokens":4,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":7}}}`,
		outputs,
	)
	return "event: response.completed\ndata: " + data
}

// collect drains ch until it closes or the deadline fires and
// returns every event received.
func collect(t *testing.T, ch <-chan StreamEvent, timeout time.Duration) []StreamEvent {
	t.Helper()
	var got []StreamEvent
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-deadline:
			t.Fatalf("timed out waiting for stream to close; got %d events", len(got))
		}
	}
}

// TestClient_Stream_ForwardsTextDeltas verifies that text deltas
// arrive on the channel in order, the channel is closed on clean
// completion, and the accumulated assistant text is appended to the
// conversation so the post-stream state matches the Send path.
func TestClient_Stream_ForwardsTextDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseResponse(w,
			textDeltaEvent("Hel"),
			textDeltaEvent("lo "),
			textDeltaEvent("world"),
			completedEvent("Hello world", "", ""),
		)
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL)
	conv := mustNewConversation(t, c, "sys", nil)
	conv.AppendUser("hi")

	ch, err := conv.Stream(t.Context(), "")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch, 5*time.Second)

	// Only text-delta events should reach the channel on clean
	// completion.
	wantDeltas := []string{"Hel", "lo ", "world"}
	if len(events) != len(wantDeltas) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(wantDeltas), events)
	}
	for i, ev := range events {
		if ev.Err != nil {
			t.Fatalf("unexpected Err on event %d: %v", i, ev.Err)
		}
		if ev.TextDelta != wantDeltas[i] {
			t.Errorf("event %d delta = %q, want %q", i, ev.TextDelta, wantDeltas[i])
		}
	}

	// Final conversation state must include the full assistant text
	// and a usage snapshot, matching Conversation.Send's semantics.
	var gotText string
	for _, tr := range conv.Turns() {
		if tr.Role == RoleAssistant {
			gotText += tr.Text
		}
	}
	if gotText != "Hello world" {
		t.Errorf("assistant turn text = %q, want %q", gotText, "Hello world")
	}
	if conv.Usage().Total == 0 {
		t.Errorf("usage not recorded after streaming completion")
	}
}

// TestClient_Stream_ForwardsReasoningDeltas verifies that reasoning
// deltas are surfaced in order on ReasoningDelta and interleave with
// TextDelta events preserving provider-side arrival order, so UIs
// can show the model "thinking" before the answer starts streaming.
// Both response.reasoning_text.delta and
// response.reasoning_summary_text.delta feed the same field.
func TestClient_Stream_ForwardsReasoningDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseResponse(w,
			reasoningDeltaEvent("response.reasoning_text.delta", "think-1 "),
			reasoningDeltaEvent("response.reasoning_summary_text.delta", "summary-1"),
			textDeltaEvent("answer"),
			completedEvent("answer", "", ""),
		)
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL)
	conv := mustNewConversation(t, c, "sys", nil)
	conv.AppendUser("hi")

	ch, err := conv.Stream(t.Context(), "")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch, 5*time.Second)

	type kind int
	const (
		kindReasoning kind = iota
		kindText
	)
	type item struct {
		k kind
		s string
	}
	want := []item{
		{kindReasoning, "think-1 "},
		{kindReasoning, "summary-1"},
		{kindText, "answer"},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
	}
	for i, ev := range events {
		if ev.Err != nil {
			t.Fatalf("unexpected Err on event %d: %v", i, ev.Err)
		}
		switch want[i].k {
		case kindReasoning:
			if ev.ReasoningDelta != want[i].s || ev.TextDelta != "" {
				t.Errorf("event %d = %+v, want ReasoningDelta=%q only", i, ev, want[i].s)
			}
		case kindText:
			if ev.TextDelta != want[i].s || ev.ReasoningDelta != "" {
				t.Errorf("event %d = %+v, want TextDelta=%q only", i, ev, want[i].s)
			}
		}
	}
}

// TestClient_Stream_CategoryFilter verifies that
// ClientConfig.StreamCategories restricts which kinds of fragments
// reach the channel without disturbing the final committed
// conversation state. Only the explicitly selected category should
// arrive; the other category's fragments should be silently dropped.
func TestClient_Stream_CategoryFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseResponse(w,
			reasoningDeltaEvent("response.reasoning_text.delta", "thinking"),
			textDeltaEvent("answer"),
			completedEvent("answer", "", ""),
		)
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL)
	c.streamCategories = StreamText // explicit override; reasoning suppressed
	conv := mustNewConversation(t, c, "sys", nil)
	conv.AppendUser("hi")

	ch, err := conv.Stream(t.Context(), "")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch, 5*time.Second)

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	if events[0].TextDelta != "answer" || events[0].ReasoningDelta != "" {
		t.Errorf("event = %+v, want TextDelta=answer only", events[0])
	}

	// Final conversation state must still include the assistant text
	// regardless of which categories were streamed.
	var gotText string
	for _, tr := range conv.Turns() {
		if tr.Role == RoleAssistant {
			gotText += tr.Text
		}
	}
	if gotText != "answer" {
		t.Errorf("assistant turn text = %q, want %q", gotText, "answer")
	}
}

// TestClient_Stream_CommitsToolCalls verifies that function_call
// items emitted by the model are recorded on conv even though they
// are not forwarded as StreamEvents, so the next Send can feed tool
// outputs back.
func TestClient_Stream_CommitsToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseResponse(w,
			textDeltaEvent("calling tool"),
			completedEvent("calling tool", "echo", `{"x":1}`),
		)
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL)
	conv := mustNewConversation(t, c, "sys", []Tool{NewFuncTool("echo", "", map[string]any{"type": "object"}, nil)})
	conv.AppendUser("please echo")

	ch, err := conv.Stream(t.Context(), "")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_ = collect(t, ch, 5*time.Second)

	var toolCall *ToolCallPayload
	for _, tr := range conv.Turns() {
		if tr.Role == RoleToolCall && tr.Tool != nil {
			toolCall = tr.Tool
			break
		}
	}
	if toolCall == nil {
		t.Fatalf("no tool_call turn recorded; turns=%+v", conv.Turns())
	}
	if toolCall.Name != "echo" || toolCall.CallID != "call_1" || toolCall.Arguments != `{"x":1}` {
		t.Errorf("tool_call = %+v, want name=echo call_1 args={\"x\":1}", toolCall)
	}
}

// TestConversation_Stream_RejectsNilClient verifies the pre-stream
// precondition is enforced synchronously so callers do not have to
// drain a nil-or-closed channel to discover a programming error.
func TestConversation_Stream_RejectsNilClient(t *testing.T) {
	conv := mustNewConversation(t, nil, "", nil)
	conv.AppendUser("hi")
	ch, err := conv.Stream(t.Context(), "")
	if err != ErrNoClient {
		t.Fatalf("err = %v, want ErrNoClient", err)
	}
	if ch != nil {
		t.Errorf("channel should be nil on pre-stream error, got %v", ch)
	}
}

// TestClient_Stream_SurfacesMidStreamFailure verifies that a
// response.failed event is converted into a terminal Err event and
// no assistant turn is appended when the stream never completes.
func TestClient_Stream_SurfacesMidStreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseResponse(w,
			textDeltaEvent("partial"),
			`event: response.failed
data: {"type":"response.failed","sequence_number":0,"response":{"id":"r1","object":"response","created_at":0,"model":"test-model","status":"failed","output":[],"parallel_tool_calls":false,"tool_choice":"auto","tools":[]}}`,
		)
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL)
	conv := mustNewConversation(t, c, "sys", nil)
	conv.AppendUser("hi")

	ch, err := conv.Stream(t.Context(), "")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch, 5*time.Second)

	if len(events) == 0 {
		t.Fatalf("no events received")
	}
	last := events[len(events)-1]
	if last.Err == nil {
		t.Errorf("want terminal Err event, got %+v", last)
	}

	for _, tr := range conv.Turns() {
		if tr.Role == RoleAssistant {
			t.Errorf("assistant turn appended despite mid-stream failure: %q", tr.Text)
		}
	}
}

// TestClient_Stream_CtxCancel verifies that cancelling ctx causes
// the internal worker to close the channel without forwarding
// further events, even when the server keeps trickling deltas.
func TestClient_Stream_CtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 100; i++ {
			_, _ = w.Write([]byte(textDeltaEvent(fmt.Sprintf("d%d", i)) + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL)
	conv := mustNewConversation(t, c, "sys", nil)
	conv.AppendUser("hi")

	ctx, cancel := context.WithCancel(t.Context())
	ch, err := conv.Stream(ctx, "")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	// Read a couple of deltas then cancel.
	<-ch
	cancel()
	// Drain until close without asserting exact counts; the
	// invariant is that the channel closes within a bounded time.
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("channel did not close after ctx cancel")
	}
}

// TestClient_Stream_SurfacesMissingCompleted verifies that a stream
// which ends without response.completed and without a transport
// error still surfaces a terminal Err event, so consumers do not
// mistake a truncated stream for a clean completion. This covers
// the case where the server closes the connection mid-stream but
// the SSE framing itself is well-formed (no stream.Err).
func TestClient_Stream_SurfacesMissingCompleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Emit a couple of deltas, then close the response without
		// ever sending response.completed.
		sseResponse(w,
			textDeltaEvent("partial"),
			textDeltaEvent(" answer"),
		)
	}))
	t.Cleanup(srv.Close)

	c := testClient(srv.URL)
	conv := mustNewConversation(t, c, "sys", nil)
	conv.AppendUser("hi")

	ch, err := conv.Stream(t.Context(), "")
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collect(t, ch, 5*time.Second)

	if len(events) == 0 {
		t.Fatalf("no events received")
	}
	last := events[len(events)-1]
	if last.Err == nil {
		t.Errorf("want terminal Err event for missing response.completed, got %+v", last)
	}
	for _, tr := range conv.Turns() {
		if tr.Role == RoleAssistant {
			t.Errorf("assistant turn appended despite missing response.completed: %q", tr.Text)
		}
	}
}
