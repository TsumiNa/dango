package builtin

import "testing"

func TestParseExtraTool(t *testing.T) {
	t.Run("known", func(t *testing.T) {
		got, err := ParseExtraTool("list_dir")
		if err != nil {
			t.Fatalf("ParseExtraTool(list_dir): %v", err)
		}
		if got != ExtraListDir {
			t.Fatalf("ParseExtraTool(list_dir) = %q, want %q", got, ExtraListDir)
		}
		if got.String() != "list_dir" {
			t.Fatalf("ExtraListDir.String() = %q, want list_dir", got.String())
		}
	})

	t.Run("unknown", func(t *testing.T) {
		if _, err := ParseExtraTool("nope"); err == nil {
			t.Fatal("expected unknown extra error")
		}
	})
}
