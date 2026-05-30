package llm

import (
	"context"
	"fmt"
	"strings"
)

// Summarizer collapses an older slice of turns into a single compact summary
// string. Implementations may be backed by a separate LLM call, but the default
// [DefaultSummarizerFunc] is deterministic and local.
//
// Summarize must be safe to call from inside [Conversation.Send] - in
// particular, it must not call [Conversation.Send] on the same conversation it
// is summarising, or it will recurse.
type Summarizer interface {
	Summarize(ctx context.Context, turns []Turn) (string, error)
}

// SummarizerFunc adapts a plain function into a [Summarizer].
type SummarizerFunc func(ctx context.Context, turns []Turn) (string, error)

// Summarize implements [Summarizer].
func (f SummarizerFunc) Summarize(ctx context.Context, turns []Turn) (string, error) {
	return f(ctx, turns)
}

// DefaultSummarizerFunc is the deterministic local summarizer used by new
// conversations unless callers provide a custom [ConversationConfig.Summarizer]
// or replace it with [Conversation.SetSummarizer].
func DefaultSummarizerFunc(ctx context.Context, turns []Turn) (string, error) {
	const maxSummaryBytes = 2400
	const maxTurnBytes = 240

	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	if len(turns) == 0 {
		return "No earlier conversation turns.", nil
	}

	var b strings.Builder
	b.WriteString("Earlier conversation:\n")
	for _, turn := range turns {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		line := summarizeTurn(turn, maxTurnBytes)
		if line == "" {
			continue
		}
		if b.Len()+len(line)+4 > maxSummaryBytes {
			b.WriteString("- ...")
			break
		}
		b.WriteString("- ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String()), nil
}

func summarizeTurn(turn Turn, maxBytes int) string {
	var text string
	if turn.Tool != nil {
		name := turn.Tool.Name
		if name == "" {
			name = turn.Tool.CallID
		}
		switch turn.Role {
		case RoleToolCall:
			text = fmt.Sprintf("tool call %s %s", name, turn.Tool.Arguments)
		case RoleToolOutput:
			text = fmt.Sprintf("tool result %s %s", name, turn.Tool.Output)
			if turn.Tool.Error != "" {
				text += " error=" + turn.Tool.Error
			}
		default:
			text = turn.Tool.Output
		}
	} else {
		text = turn.Text
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	if len(text) > maxBytes {
		text = text[:maxBytes] + "..."
	}
	return fmt.Sprintf("%s: %s", turn.Role, text)
}
