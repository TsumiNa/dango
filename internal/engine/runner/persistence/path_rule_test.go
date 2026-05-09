package persistence

import "testing"

func TestDefaultPathRule(t *testing.T) {
	if got := DefaultPathRule("runner-1"); got != "task_runner-1" {
		t.Fatalf("DefaultPathRule() = %q, want %q", got, "task_runner-1")
	}
}
