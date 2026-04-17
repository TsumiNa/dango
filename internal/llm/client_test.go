package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func TestProviderBaseURL(t *testing.T) {
	cases := map[Provider]string{
		ProviderOpenAI:     "",
		ProviderOpenRouter: "https://openrouter.ai/api/v1/",
		ProviderGemini:     "https://generativelanguage.googleapis.com/v1beta/openai/",
	}
	for p, want := range cases {
		if got := p.baseURL(); got != want {
			t.Errorf("%s.baseURL() = %q, want %q", p, got, want)
		}
	}
}

func TestDetectProviderPriority(t *testing.T) {
	tests := []struct {
		name         string
		env          map[string]string
		wantProvider Provider
		wantKey      string
		wantOK       bool
	}{
		{
			name:         "none",
			env:          nil,
			wantProvider: "",
			wantOK:       false,
		},
		{
			name:         "openai_only",
			env:          map[string]string{"OPENAI_API_KEY": "oai"},
			wantProvider: ProviderOpenAI,
			wantKey:      "oai",
			wantOK:       true,
		},
		{
			name:         "openai_wins_over_others",
			env:          map[string]string{"OPENAI_API_KEY": "oai", "OPENROUTER_API_KEY": "or", "GEMINI_API_KEY": "gm"},
			wantProvider: ProviderOpenAI,
			wantKey:      "oai",
			wantOK:       true,
		},
		{
			name:         "openrouter_over_gemini",
			env:          map[string]string{"OPENROUTER_API_KEY": "or", "GEMINI_API_KEY": "gm"},
			wantProvider: ProviderOpenRouter,
			wantKey:      "or",
			wantOK:       true,
		},
		{
			name:         "gemini_only",
			env:          map[string]string{"GEMINI_API_KEY": "gm"},
			wantProvider: ProviderGemini,
			wantKey:      "gm",
			wantOK:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearProviderEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			p, key, ok := detectProvider()
			if ok != tt.wantOK || p != tt.wantProvider || key != tt.wantKey {
				t.Fatalf("detectProvider() = (%q, %q, %v), want (%q, %q, %v)",
					p, key, ok, tt.wantProvider, tt.wantKey, tt.wantOK)
			}
		})
	}
}

func TestNewClientFromEnv_NoAPIKey(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("ORCHESTRATION_MODEL", "gpt-test")
	if _, err := NewClientFromEnv(); err != ErrNoAPIKey {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
}

func TestNewClientFromEnv_NoModel(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENAI_API_KEY", "oai")
	t.Setenv("ORCHESTRATION_MODEL", "")
	if _, err := NewClientFromEnv(); err != ErrNoModel {
		t.Fatalf("err = %v, want ErrNoModel", err)
	}
}

func TestNewClientFromEnv_Success(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	t.Setenv("ORCHESTRATION_MODEL", "some-model")

	c, err := NewClientFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Provider() != ProviderOpenRouter {
		t.Errorf("Provider() = %s, want %s", c.Provider(), ProviderOpenRouter)
	}
	if c.Model() != "some-model" {
		t.Errorf("Model() = %s, want some-model", c.Model())
	}
	if !strings.Contains(c.String(), "openrouter") || !strings.Contains(c.String(), "some-model") {
		t.Errorf("String() = %q, missing provider/model", c.String())
	}
	if c.Raw() == nil {
		t.Error("Raw() returned nil")
	}
}

func TestClient_Respond(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		// Minimal Response payload with one output_text item.
		_, _ = w.Write([]byte(`{
			"id": "resp_1",
			"object": "response",
			"created_at": 0,
			"model": "test-model",
			"status": "completed",
			"output": [
				{
					"id": "msg_1",
					"type": "message",
					"role": "assistant",
					"status": "completed",
					"content": [
						{"type": "output_text", "text": "hello world", "annotations": []}
					]
				}
			],
			"parallel_tool_calls": false,
			"tool_choice": "auto",
			"tools": []
		}`))
	}))
	t.Cleanup(srv.Close)

	c := &Client{
		provider: ProviderOpenAI,
		model:    "test-model",
		raw: openai.NewClient(
			option.WithAPIKey("test-key"),
			option.WithBaseURL(srv.URL+"/"),
		),
	}

	out, err := c.Respond(t.Context(), "hi")
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if out != "hello world" {
		t.Errorf("Respond() = %q, want %q", out, "hello world")
	}
	if !strings.HasSuffix(gotPath, "/responses") {
		t.Errorf("request path = %q, want suffix /responses", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization header = %q", gotAuth)
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(gotBody), &req); err != nil {
		t.Fatalf("request body not JSON: %v (%s)", err, gotBody)
	}
	if req["model"] != "test-model" {
		t.Errorf("request model = %v, want test-model", req["model"])
	}
	if req["input"] != "hi" {
		t.Errorf("request input = %v, want \"hi\"", req["input"])
	}
}

// clearProviderEnv wipes the environment variables that NewClientFromEnv and
// detectProvider consult, isolating tests from the developer's real shell
// environment (including any .env file loaded via godotenv.Load).
func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "GEMINI_API_KEY", "ORCHESTRATION_MODEL"} {
		t.Setenv(k, "")
	}
}
