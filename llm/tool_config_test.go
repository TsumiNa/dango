package llm

import "testing"

func TestParseExtraTool(t *testing.T) {
	got, err := ParseExtraTool("pwd")
	if err != nil {
		t.Fatalf("ParseExtraTool(pwd): %v", err)
	}
	if got != ExtraPwd {
		t.Fatalf("ParseExtraTool(pwd) = %q, want %q", got, ExtraPwd)
	}
}
