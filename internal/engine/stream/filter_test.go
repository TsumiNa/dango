package stream

import "testing"

func TestFilterMatch(t *testing.T) {
	event := Event{
		EventType: EventLLMReasoningDelta,
		From:      Source{Layer: "conversation", ID: "conv_1", ParentID: "exec_1"},
		Status:    StatusRunning,
		Scope:     Scope{RequestID: "req_1", RunnerID: "run_1", NodeID: "node_1"},
	}

	tests := []struct {
		name   string
		filter Filter
		want   bool
	}{
		{
			name: "empty matches all",
			want: true,
		},
		{
			name:   "exact event type",
			filter: Filter{EventTypes: []string{EventLLMReasoningDelta}},
			want:   true,
		},
		{
			name:   "event type prefix",
			filter: Filter{Prefixes: []string{"llm."}},
			want:   true,
		},
		{
			name:   "source selector",
			filter: Filter{Sources: []SourceSelector{{Layer: "conversation", ParentID: "exec_1"}}},
			want:   true,
		},
		{
			name:   "status selector",
			filter: Filter{Statuses: []string{StatusRunning}},
			want:   true,
		},
		{
			name:   "scope selector",
			filter: Filter{Scope: Scope{RunnerID: "run_1", NodeID: "node_1"}},
			want:   true,
		},
		{
			name:   "wrong prefix",
			filter: Filter{Prefixes: []string{"runner."}},
			want:   false,
		},
		{
			name:   "wrong source",
			filter: Filter{Sources: []SourceSelector{{Layer: "executor"}}},
			want:   false,
		},
		{
			name:   "wrong status",
			filter: Filter{Statuses: []string{StatusCompleted}},
			want:   false,
		},
		{
			name:   "wrong scope",
			filter: Filter{Scope: Scope{RunnerID: "other"}},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.filter.Match(event); got != tt.want {
				t.Fatalf("Match = %v, want %v", got, tt.want)
			}
		})
	}
}
