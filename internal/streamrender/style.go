package streamrender

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

// kv renders a structured key/value pair. The key is colored when enabled so
// the eye can align quickly, while the value remains plain so values like
// status=failed stay greppable.
func (r *Renderer) kv(key, value string) string {
	return r.colorKey(key) + "=" + value
}

func (r *Renderer) kvQuoted(key, value string) string {
	return r.colorKey(key) + "=" + fmt.Sprintf("%q", value)
}

// tag renders an inline word marker such as "reasoning" or "output". It is
// colored differently from the structured keys so a glance at the line shows
// which kind of free-form payload follows the dot separator.
func (r *Renderer) tag(name string) string {
	if !r.cfg.Color {
		return name
	}
	return r.lip.NewStyle().Foreground(lipgloss.Color("3")).Render(name)
}

func (r *Renderer) colorKey(key string) string {
	if !r.cfg.Color {
		return key
	}
	return r.lip.NewStyle().Foreground(lipgloss.Color("6")).Render(key)
}

func (r *Renderer) colorIdent(text string) string {
	if !r.cfg.Color {
		return text
	}
	return r.lip.NewStyle().Foreground(lipgloss.Color("3")).Render(text)
}

func (r *Renderer) colorLayer(layer, text string) string {
	if !r.cfg.Color {
		return text
	}
	color := layerColor(layer)
	return r.lip.NewStyle().Foreground(lipgloss.Color(fmt.Sprint(color))).Render(text)
}

// colorPath highlights a file path or file:// URL so the eye can spot
// jump-to-file references in a busy stream. Underline + bright cyan also
// happens to be what most modern terminals visually treat as a hyperlink, so
// the styling reads as "this is something you can open" even without explicit
// OSC 8 hyperlink escapes.
func (r *Renderer) colorPath(text string) string {
	if !r.cfg.Color {
		return text
	}
	return r.lip.NewStyle().Foreground(lipgloss.Color("14")).Underline(true).Render(text)
}

func (r *Renderer) statusIcon(status string) string {
	switch status {
	case streampkg.StatusFailed:
		return r.colorStatus(status, "✗")
	case streampkg.StatusCompleted:
		return r.colorStatus(status, "✓")
	case streampkg.StatusRunning:
		return r.colorStatus(status, "●")
	case streampkg.StatusPending:
		return r.colorStatus(status, "○")
	default:
		return r.dim("·")
	}
}

func (r *Renderer) colorStatus(status, text string) string {
	if !r.cfg.Color {
		return text
	}
	return r.lip.NewStyle().Foreground(lipgloss.Color(fmt.Sprint(statusColor(status)))).Render(text)
}

func (r *Renderer) dim(text string) string {
	if !r.cfg.Color {
		return text
	}
	return r.lip.NewStyle().Faint(true).Render(text)
}

func statusColor(status string) int {
	switch status {
	case streampkg.StatusFailed:
		return 31 // red
	case streampkg.StatusCompleted:
		return 32 // green
	case streampkg.StatusRunning:
		return 36 // cyan
	case streampkg.StatusPending:
		return 90 // dim
	default:
		return 37 // white
	}
}

func layerColor(layer string) int {
	switch layer {
	case "orchestrator":
		return 35 // magenta
	case "runner":
		return 36 // cyan
	case "agent":
		return 34 // blue
	case "skill":
		return 32 // green
	default:
		return 37
	}
}

// layerName returns the canonical lower-case layer key together with its
// human-readable label. Layers from unknown sources fall back to a Title-cased
// version of the raw string so future event sources are still legible.
func layerName(layer string) (string, string) {
	switch layer {
	case "orchestrator":
		return layer, "Orchestrator"
	case "runner":
		return layer, "Runner"
	case "agent":
		return layer, "Agent"
	case "skill":
		return layer, "Skill"
	case "":
		return "", "Event"
	default:
		return layer, strings.ToUpper(layer[:1]) + layer[1:]
	}
}

func layerIdentifier(event streampkg.Event) string {
	switch event.From.Layer {
	case "skill":
		if name := skillName(event); name != "" && name != "unknown" {
			return name
		}
		return event.From.ID
	case "runner":
		return shortRunnerID(event.Scope.RunnerID)
	case "agent":
		return event.From.ID
	default:
		return ""
	}
}

func shortRunnerID(id string) string {
	if len(id) > defaultRunnerIDClip {
		return id[:defaultRunnerIDClip] + "…"
	}
	return id
}
