package orchestrate

import "testing"

func TestRequestPriorityValid(t *testing.T) {
	tests := []struct {
		name     string
		priority RequestPriority
		want     bool
	}{
		{name: "default zero value", priority: RequestPriorityDefault, want: true},
		{name: "highest", priority: RequestPriorityHighest, want: true},
		{name: "middle", priority: 2, want: true},
		{name: "below range", priority: -1, want: false},
		{name: "above range", priority: RequestPriorityHighest + 1, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.priority.valid(); got != tt.want {
				t.Fatalf("priority.valid() = %v, want %v", got, tt.want)
			}
		})
	}
}
