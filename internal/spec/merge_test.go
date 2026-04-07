package spec

import "testing"

func TestMergeToolSpec(t *testing.T) {
	t.Parallel()

	base := ToolSpec{
		Name:        "pdf-generator",
		Version:     "1.0.0",
		Description: "Generates PDF reports",
		InputTypes:  []string{"json", "csv"},
		OutputTypes: []string{"pdf"},
		Model:       "local/pdf-specialist-v2",
		Defaults: map[string]any{
			"page_size": "A4",
			"language":  "ja",
			"limits": map[string]any{
				"max_pages": 100,
			},
		},
	}

	override := map[string]any{
		"model": "openrouter/google/gemini-2.5-flash",
		"defaults": map[string]any{
			"page_size": "letter",
			"limits": map[string]any{
				"max_pages": 12,
			},
		},
	}

	merged, err := MergeToolSpec(base, override)
	if err != nil {
		t.Fatalf("MergeToolSpec() error = %v", err)
	}

	if got, want := merged.Model, "openrouter/google/gemini-2.5-flash"; got != want {
		t.Fatalf("merged.Model = %q, want %q", got, want)
	}
	if got, want := merged.Defaults["page_size"], "letter"; got != want {
		t.Fatalf("merged.Defaults[page_size] = %#v, want %q", got, want)
	}
	if got, want := merged.Defaults["language"], "ja"; got != want {
		t.Fatalf("merged.Defaults[language] = %#v, want %q", got, want)
	}

	limits, ok := merged.Defaults["limits"].(map[string]any)
	if !ok {
		t.Fatalf("merged.Defaults[limits] is %T, want map[string]any", merged.Defaults["limits"])
	}
	if got, want := limits["max_pages"], 12; got != want {
		t.Fatalf("merged.Defaults[limits][max_pages] = %#v, want %d", got, want)
	}
}
