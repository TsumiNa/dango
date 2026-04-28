package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestDemoPlannerInputFromOpenAIRequest(t *testing.T) {
	prompt := `{"mode":"plan","data":{"request":"ship"}}`
	tests := map[string]string{
		"string input":           `{"input":` + strconv.Quote(prompt) + `}`,
		"message content string": `{"input":[{"role":"user","content":` + strconv.Quote(prompt) + `}]}`,
		"message content parts":  `{"input":[{"role":"assistant","content":"old"},{"role":"user","content":[{"type":"input_text","text":` + strconv.Quote(prompt) + `}]}]}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := demoPlannerInputFromOpenAIRequest(strings.NewReader(body))
			if err != nil {
				t.Fatalf("demoPlannerInputFromOpenAIRequest: %v", err)
			}
			if got != prompt {
				t.Fatalf("input = %q, want %q", got, prompt)
			}
		})
	}
}

func TestDemoPlannerInputFromOpenAIRequestRejectsMissingUserText(t *testing.T) {
	_, err := demoPlannerInputFromOpenAIRequest(strings.NewReader(`{"input":[{"role":"assistant","content":"old"}]}`))
	if err == nil {
		t.Fatal("demoPlannerInputFromOpenAIRequest returned nil error")
	}
}
