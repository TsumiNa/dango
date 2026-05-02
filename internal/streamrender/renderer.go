package streamrender

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

const defaultMaxText = 240

// Config controls how stream events are rendered for terminal output.
type Config struct {
	// Filter optionally limits which events the renderer prints. Subscribing
	// with the same filter is still preferred when callers want to reduce event
	// delivery work; this field is useful for last-mile CLI preferences.
	Filter streampkg.Filter

	// HiddenEventTypes suppresses noisy event types after Filter matching.
	HiddenEventTypes []string

	// Color enables ANSI color in labels and status markers.
	Color bool

	// MaxText caps long delta fields. Zero uses a conservative default.
	MaxText int

	// ProgressFrames are appended to running events to make repeated progress
	// visibly alive. Nil uses ASCII spinner frames.
	ProgressFrames []string

	// DedupeRepeated suppresses adjacent identical rendered lines.
	DedupeRepeated bool

	// ExchangeDir, when non-empty, receives full exchange markdown payloads
	// found in string deltas. The terminal line links to the written file.
	ExchangeDir string

	// InlineImages emits iTerm2-compatible inline image escape sequences for
	// image artifact paths. Terminals that do not support the protocol will still
	// see the file URL line before the escape sequence.
	InlineImages bool

	// ImageMaxBytes caps inline image reads. Zero uses a small default.
	ImageMaxBytes int
}

// DefaultConfig returns a compact, deterministic renderer configuration.
func DefaultConfig() Config {
	return Config{
		MaxText:          defaultMaxText,
		ProgressFrames:   []string{"|", "/", "-", "\\"},
		DedupeRepeated:   true,
		ImageMaxBytes:    2 << 20,
		HiddenEventTypes: nil,
	}
}

// Renderer writes compact terminal lines for stream events.
type Renderer struct {
	out      io.Writer
	cfg      Config
	lastLine string
	frame    int
}

// New creates a Renderer that writes to out. A nil writer discards output.
func New(out io.Writer, cfg Config) *Renderer {
	if out == nil {
		out = io.Discard
	}
	if cfg.MaxText <= 0 {
		cfg.MaxText = defaultMaxText
	}
	if cfg.ProgressFrames == nil {
		cfg.ProgressFrames = []string{"|", "/", "-", "\\"}
	}
	if cfg.ImageMaxBytes <= 0 {
		cfg.ImageMaxBytes = 2 << 20
	}
	return &Renderer{out: out, cfg: cfg}
}

// RenderSubscription drains sub until it closes or ctx is canceled.
func (r *Renderer) RenderSubscription(ctx context.Context, sub *streampkg.Subscription) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		event, ok, err := sub.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := r.RenderEvent(event); err != nil {
			return err
		}
	}
}

// RenderEvent formats event and writes one or more terminal lines.
func (r *Renderer) RenderEvent(event streampkg.Event) error {
	line := r.FormatEvent(event)
	if line == "" {
		return nil
	}
	if r.cfg.DedupeRepeated && line == r.lastLine {
		return nil
	}
	_, err := fmt.Fprintln(r.out, line)
	if err == nil {
		r.lastLine = line
	}
	return err
}

// FormatEvent returns the terminal line for event, or an empty string when the
// event is filtered or intentionally silent.
func (r *Renderer) FormatEvent(event streampkg.Event) string {
	if r == nil || !r.shouldRender(event) {
		return ""
	}
	values := deltaMap(event)
	line := r.formatKnownEvent(event, values)
	if line == "" && knownEventType(event.EventType) {
		return ""
	}
	if line == "" {
		line = r.formatGenericEvent(event, values)
	}
	if line == "" {
		return ""
	}
	if event.Status == streampkg.StatusRunning {
		line += " " + r.nextFrame()
	}
	return line
}

func knownEventType(eventType string) bool {
	switch eventType {
	case streampkg.EventLLMReasoningDelta, streampkg.EventLLMOutputDelta,
		streampkg.EventStatusStarted, streampkg.EventStatusProgress, streampkg.EventStatusCompleted, streampkg.EventStatusFailed,
		streampkg.EventRunnerPhaseChanged,
		streampkg.EventRunnerNodeStarted, streampkg.EventRunnerNodeCompleted, streampkg.EventRunnerNodeFailed,
		streampkg.EventExecutorPolishStarted, streampkg.EventExecutorPolishCompleted, streampkg.EventExecutorPolishFailed,
		streampkg.EventExecutorExecuteStarted, streampkg.EventExecutorExecuteCompleted, streampkg.EventExecutorExecuteFailed,
		streampkg.EventExecutorReportStarted, streampkg.EventExecutorReportCompleted, streampkg.EventExecutorReportFailed,
		streampkg.EventSkillMemoDelta,
		streampkg.EventArtifactCreated,
		streampkg.EventLLMToolCallStarted, streampkg.EventLLMToolCallDelta, streampkg.EventLLMToolCallCompleted,
		streampkg.EventToolExecutionStarted, streampkg.EventToolExecutionCompleted, streampkg.EventToolExecutionFailed,
		streampkg.EventLLMToolResultDelta:
		return true
	default:
		return false
	}
}

func (r *Renderer) shouldRender(event streampkg.Event) bool {
	if !r.cfg.Filter.Match(event) {
		return false
	}
	for _, hidden := range r.cfg.HiddenEventTypes {
		if hidden == event.EventType {
			return false
		}
	}
	return true
}

func (r *Renderer) formatKnownEvent(event streampkg.Event, values map[string]any) string {
	switch event.EventType {
	case streampkg.EventLLMReasoningDelta:
		return r.formatTextDelta(event, "reasoning")
	case streampkg.EventLLMOutputDelta:
		return r.formatTextDelta(event, "output")
	case streampkg.EventStatusStarted, streampkg.EventStatusProgress, streampkg.EventStatusCompleted, streampkg.EventStatusFailed:
		return r.formatStatusEvent(event, values)
	case streampkg.EventRunnerPhaseChanged:
		phase := stringValue(values["phase"])
		status := stringValue(values["status"])
		if phase == "" {
			phase = "unknown"
		}
		return fmt.Sprintf("%s runner_id=%s status=%s phase=%s", r.label("ru"), event.Scope.RunnerID, status, phase)
	case streampkg.EventRunnerNodeStarted, streampkg.EventRunnerNodeCompleted, streampkg.EventRunnerNodeFailed:
		return r.formatNodeEvent(event, values, "ru", strings.TrimPrefix(event.EventType, "runner."))
	case streampkg.EventExecutorPolishStarted, streampkg.EventExecutorPolishCompleted, streampkg.EventExecutorPolishFailed,
		streampkg.EventExecutorExecuteStarted, streampkg.EventExecutorExecuteCompleted, streampkg.EventExecutorExecuteFailed,
		streampkg.EventExecutorReportStarted, streampkg.EventExecutorReportCompleted, streampkg.EventExecutorReportFailed:
		return r.formatNodeEvent(event, values, "ex", strings.TrimPrefix(event.EventType, "executor."))
	case streampkg.EventSkillMemoDelta:
		return r.formatSkillMemo(event, values)
	case streampkg.EventArtifactCreated:
		return r.formatArtifact(event, values)
	case streampkg.EventLLMToolCallStarted, streampkg.EventLLMToolCallCompleted:
		return r.formatToolCall(event, values)
	case streampkg.EventToolExecutionStarted, streampkg.EventToolExecutionCompleted, streampkg.EventToolExecutionFailed:
		return r.formatToolExecution(event, values)
	case streampkg.EventLLMToolResultDelta:
		return r.formatToolResult(event, values)
	default:
		return ""
	}
}

func (r *Renderer) formatTextDelta(event streampkg.Event, name string) string {
	text, ok := deltaString(event)
	if !ok || strings.TrimSpace(text) == "" {
		return ""
	}
	if exchangeText(text) {
		return fmt.Sprintf("%s %s exchange=%s", r.label(layerLabel(event.From.Layer)), name, r.exchangeReference(event, text))
	}
	if event.From.Layer == "orchestrator" && stringValue(event.Metadata["stage"]) == "planning" && name == "output" {
		return fmt.Sprintf("%s planning output captured status=%s", r.label("or"), event.Status)
	}
	text, truncated := r.compact(text)
	line := fmt.Sprintf("%s %s: %s", r.label(layerLabel(event.From.Layer)), name, text)
	if truncated {
		line += " truncated=true"
	}
	return line
}

func (r *Renderer) formatStatusEvent(event streampkg.Event, values map[string]any) string {
	message := stringValue(values["message"])
	if message == "" {
		if errorText := stringValue(values["error"]); errorText != "" {
			message = errorText
		}
	}
	if message == "" {
		if text, ok := deltaString(event); ok {
			message = text
		}
	}
	if message == "" && event.From.Layer == "skill" && event.EventType == streampkg.EventStatusCompleted {
		return ""
	}
	message, truncated := r.compact(message)
	label := r.label(layerLabel(event.From.Layer))
	line := label
	if message != "" {
		line += " " + message
	}
	line += fmt.Sprintf(" status=%s event=%s", event.Status, event.EventType)
	if runnerID := stringValue(values["runner_id"]); runnerID != "" {
		line += " runner_id=" + runnerID
	}
	if usage, ok := values["usage"].(map[string]any); ok {
		if total := stringValue(usage["total_tokens"]); total != "" {
			line += " total_tokens=" + total
		}
	}
	if truncated {
		line += " truncated=true"
	}
	return line
}

func (r *Renderer) formatNodeEvent(event streampkg.Event, values map[string]any, label string, eventName string) string {
	nodeID := stringValue(values["node_id"])
	if nodeID == "" {
		nodeID = event.Scope.NodeID
	}
	line := fmt.Sprintf("%s runner_id=%s status=%s event=%s node=%s", r.label(label), event.Scope.RunnerID, event.Status, eventName, nodeID)
	if skill := skillName(event); skill != "" && skill != "unknown" {
		line += " skill=" + skill
	}
	if errText := stringValue(values["error"]); errText != "" {
		errText, _ = r.compact(errText)
		line += fmt.Sprintf(" error=%q", errText)
	}
	return line
}

func (r *Renderer) formatSkillMemo(event streampkg.Event, values map[string]any) string {
	memo, truncated := r.compact(stringValue(values["memo"]))
	if memo == "" {
		return ""
	}
	line := fmt.Sprintf("%s skill=%s status=%s event=memo node=%s", r.label("sk"), skillName(event), event.Status, nodeID(event, values))
	if stage := stringValue(values["stage"]); stage != "" {
		line += " stage=" + stage
	}
	line += fmt.Sprintf(" memo=%q", memo)
	if truncated || boolValue(values["truncated"]) {
		line += " truncated=true"
	}
	return line
}

func (r *Renderer) formatArtifact(event streampkg.Event, values map[string]any) string {
	path := stringValue(values["path"])
	if path == "" {
		return ""
	}
	line := fmt.Sprintf("%s artifact=%s", r.label("ex"), fileURL(path))
	if resourceType := stringValue(values["resource_type"]); resourceType != "" {
		line += " type=" + resourceType
	}
	if stage := stringValue(values["stage"]); stage != "" {
		line += " stage=" + stage
	}
	if desc := stringValue(values["description"]); desc != "" {
		desc, _ = r.compact(desc)
		line += fmt.Sprintf(" description=%q", desc)
	}
	if inline := r.inlineImage(path); inline != "" {
		line += "\n" + inline
	}
	return line
}

func (r *Renderer) formatToolCall(event streampkg.Event, values map[string]any) string {
	line := fmt.Sprintf("%s skill=%s status=%s event=%s tool=%s call=%s",
		r.label("sk"), skillName(event), event.Status, strings.TrimPrefix(event.EventType, "llm."), stringValue(values["name"]), stringValue(values["call_id"]))
	if args := stringValue(values["arguments"]); args != "" {
		args, _ = r.compact(args)
		line += fmt.Sprintf(" args=%q", args)
	}
	return line
}

func (r *Renderer) formatToolExecution(event streampkg.Event, values map[string]any) string {
	line := fmt.Sprintf("%s skill=%s status=%s event=%s tool=%s call=%s",
		r.label("sk"), skillName(event), event.Status, strings.TrimPrefix(event.EventType, "tool."), stringValue(values["name"]), stringValue(values["call_id"]))
	if errText := stringValue(values["error"]); errText != "" {
		errText, _ = r.compact(errText)
		line += fmt.Sprintf(" error=%q", errText)
	}
	return line
}

func (r *Renderer) formatToolResult(event streampkg.Event, values map[string]any) string {
	line := fmt.Sprintf("%s skill=%s status=%s event=tool_result tool=%s call=%s",
		r.label("sk"), skillName(event), event.Status, stringValue(values["name"]), stringValue(values["call_id"]))
	if errText := stringValue(values["error"]); errText != "" {
		errText, _ = r.compact(errText)
		line += fmt.Sprintf(" error=%q", errText)
	}
	return line
}

func (r *Renderer) formatGenericEvent(event streampkg.Event, values map[string]any) string {
	if text, ok := deltaString(event); ok {
		text, truncated := r.compact(text)
		line := fmt.Sprintf("%s status=%s event=%s delta=%q", r.label(layerLabel(event.From.Layer)), event.Status, event.EventType, text)
		if truncated {
			line += " truncated=true"
		}
		return line
	}
	if len(values) == 0 {
		return ""
	}
	return fmt.Sprintf("%s status=%s event=%s", r.label(layerLabel(event.From.Layer)), event.Status, event.EventType)
}

func (r *Renderer) compact(text string) (string, bool) {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= r.cfg.MaxText {
		return text, false
	}
	return text[:r.cfg.MaxText] + "...", true
}

func (r *Renderer) nextFrame() string {
	if len(r.cfg.ProgressFrames) == 0 {
		return ""
	}
	frame := r.cfg.ProgressFrames[r.frame%len(r.cfg.ProgressFrames)]
	r.frame++
	return frame
}

func (r *Renderer) label(text string) string {
	if text == "" {
		text = "ev"
	}
	text += ":"
	if !r.cfg.Color {
		return text
	}
	color := "36"
	switch strings.TrimSuffix(text, ":") {
	case "or":
		color = "35"
	case "ru":
		color = "36"
	case "ex":
		color = "34"
	case "sk":
		color = "32"
	}
	return "\x1b[" + color + "m" + text + "\x1b[0m"
}

func (r *Renderer) exchangeReference(event streampkg.Event, text string) string {
	if r.cfg.ExchangeDir == "" {
		return fmt.Sprintf("inline:%d-bytes", len(text))
	}
	if err := os.MkdirAll(r.cfg.ExchangeDir, 0o755); err != nil {
		return fmt.Sprintf("write-error:%s", err)
	}
	path := filepath.Join(r.cfg.ExchangeDir, fmt.Sprintf("exchange-%012d.md", event.SequenceNumber))
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Sprintf("write-error:%s", err)
	}
	return fileURL(path)
}

func (r *Renderer) inlineImage(path string) string {
	if !r.cfg.InlineImages || !imagePath(path) {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > r.cfg.ImageMaxBytes {
		return ""
	}
	name := base64.StdEncoding.EncodeToString([]byte(filepath.Base(path)))
	payload := base64.StdEncoding.EncodeToString(data)
	return "\x1b]1337;File=name=" + name + ";inline=1:" + payload + "\a"
}

func deltaString(event streampkg.Event) (string, bool) {
	var text string
	if err := json.Unmarshal(event.Delta, &text); err != nil {
		return "", false
	}
	return text, true
}

func deltaMap(event streampkg.Event) map[string]any {
	var values map[string]any
	if err := json.Unmarshal(event.Delta, &values); err != nil {
		return nil
	}
	return values
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case float64:
		return fmt.Sprintf("%.0f", v)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
}

func skillName(event streampkg.Event) string {
	if skill := stringValue(event.Metadata["skill_name"]); skill != "" {
		return skill
	}
	if event.From.ID != "" {
		return event.From.ID
	}
	return "unknown"
}

func nodeID(event streampkg.Event, values map[string]any) string {
	if id := stringValue(values["node_id"]); id != "" {
		return id
	}
	return event.Scope.NodeID
}

func layerLabel(layer string) string {
	switch layer {
	case "orchestrator":
		return "or"
	case "runner":
		return "ru"
	case "executor":
		return "ex"
	case "skill":
		return "sk"
	default:
		return layer
	}
}

func fileURL(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func imagePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func exchangeText(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "---") && strings.Contains(trimmed, "kind: dango.exchange")
}
