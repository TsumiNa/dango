package prompts

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/tsumina/dango/internal/taskflow"
)

//go:embed intent_understand.md
var intentUnderstandPrompt string

type intentPromptInput struct {
	RequestJSON string
	EntryJSON   string
}

// RenderIntentUnderstand renders the built-in orchestrator
// intent-understanding prompt.
//
// The prompt contains both the normalized request payload and the captured
// request-entry metadata so the model can rewrite the request while preserving
// control-plane context such as ingress surface, listener metadata, and the
// original multimodal request structure.
func RenderIntentUnderstand(request taskflow.RequestEnvelope, entry taskflow.RequestMetadata) (string, error) {
	requestPayload, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal request for intent prompt: %w", err)
	}
	entryPayload, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal entry metadata for intent prompt: %w", err)
	}

	tmpl, err := template.New("intent_understand").Parse(intentUnderstandPrompt)
	if err != nil {
		return "", fmt.Errorf("parse intent prompt template: %w", err)
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, intentPromptInput{
		RequestJSON: string(requestPayload),
		EntryJSON:   string(entryPayload),
	}); err != nil {
		return "", fmt.Errorf("render intent prompt: %w", err)
	}
	return strings.TrimSpace(out.String()), nil
}
