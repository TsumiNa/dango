package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	streampkg "github.com/tsumina/dango/stream"
)

func TestConversationSend_EmitsCompletionEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"r_send","object":"response","created_at":0,"model":"m","status":"completed",
			"output":[{
				"id":"m1","type":"message","role":"assistant","status":"completed",
				"content":[{"type":"output_text","text":"ok","annotations":[]}]
			}],
			"parallel_tool_calls":false,"tool_choice":"auto","tools":[],
			"usage":{
				"input_tokens":50,"input_tokens_details":{"cached_tokens":10},
				"output_tokens":5,"output_tokens_details":{"reasoning_tokens":2},
				"total_tokens":55
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	conv := mustNewConversation(t, testClient(srv.URL), "sys", nil, ConversationConfig{
		StreamEvents: true,
		StreamSource: streampkg.Source{Layer: "skill", ID: "send_skill"},
		StreamScope:  streampkg.Scope{RequestID: "req_send", NodeID: "node_send"},
	})
	eventStream := conv.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventStatusCompleted}}, streampkg.WithSubscriberBuffer(4))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	conv.AppendUser("hi")

	if _, err := conv.Send(t.Context(), ""); err != nil {
		t.Fatalf("Send: %v", err)
	}
	eventStream.Close()

	for _, event := range collectStreamEvents(t, sub) {
		if event.EventType != streampkg.EventStatusCompleted || event.From.Layer != "skill" || event.Scope.NodeID != "node_send" {
			continue
		}
		var delta map[string]any
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			t.Fatalf("unmarshal completion delta: %v", err)
		}
		usage, _ := delta["usage"].(map[string]any)
		if delta["response_id"] != "r_send" || delta["model"] != "m" || delta["has_text"] != true || delta["tool_call_count"] != float64(0) || usage["total_tokens"] != float64(55) || usage["cached_tokens"] != float64(10) {
			t.Fatalf("completion delta = %+v", delta)
		}
		return
	}
	t.Fatalf("missing completion event")
}

func TestConversationSend_EmitsFailureEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream exploded","type":"server_error"}}`))
	}))
	t.Cleanup(srv.Close)

	conv := mustNewConversation(t, testClient(srv.URL), "sys", nil, ConversationConfig{
		StreamEvents: true,
		StreamSource: streampkg.Source{Layer: "skill", ID: "send_skill"},
		StreamScope:  streampkg.Scope{RequestID: "req_send_fail", NodeID: "node_send"},
	})
	eventStream := conv.EventStream()
	sub, err := eventStream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventStatusFailed}}, streampkg.WithSubscriberBuffer(4))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	conv.AppendUser("hi")

	if _, err := conv.Send(t.Context(), ""); err == nil {
		t.Fatal("expected Send error")
	}
	eventStream.Close()

	if !hasStreamEvent(collectStreamEvents(t, sub), streampkg.EventStatusFailed, "skill", "node_send", func(delta map[string]any) bool {
		errorText, _ := delta["error"].(string)
		return strings.Contains(errorText, "upstream exploded")
	}) {
		t.Fatalf("missing failed send event")
	}
}
