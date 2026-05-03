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

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

const (
	defaultMaxText      = 320
	defaultRunnerIDClip = 8
	reasoningFlushBytes = 240
)

// Config controls how stream events are rendered for terminal output.
type Config struct {
	// Filter optionally limits which events the renderer prints. Subscribing
	// with the same filter is still preferred when callers want to reduce event
	// delivery work; this field is useful for last-mile CLI preferences.
	Filter streampkg.Filter

	// HiddenEventTypes suppresses noisy event types after Filter matching.
	HiddenEventTypes []string

	// Color enables ANSI color in labels, status icons, and key markers.
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

// DefaultConfig returns a compact, deterministic renderer configuration that
// surfaces orchestrator planning, runner phases, executor stages, skill memos,
// and LLM reasoning/output deltas. Low-level tool noise is suppressed.
func DefaultConfig() Config {
	return Config{
		MaxText:        defaultMaxText,
		ProgressFrames: []string{"|", "/", "-", "\\"},
		DedupeRepeated: true,
		ImageMaxBytes:  2 << 20,
		HiddenEventTypes: []string{
			streampkg.EventLLMToolCallStarted,
			streampkg.EventLLMToolCallDelta,
			streampkg.EventLLMToolCallCompleted,
			streampkg.EventLLMToolResultDelta,
			streampkg.EventToolExecutionStarted,
			streampkg.EventToolExecutionCompleted,
			streampkg.EventToolExecutionFailed,
		},
	}
}

// Renderer writes compact terminal lines for stream events.
type Renderer struct {
	out               io.Writer
	cfg               Config
	lastLine          string
	frame             int
	runningOutputSeen map[string]bool
	reasoningBuffers  map[string]*runningTextBuffer
}

type runningTextBuffer struct {
	text    string
	emitted int
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
	return &Renderer{
		out:               out,
		cfg:               cfg,
		runningOutputSeen: map[string]bool{},
		reasoningBuffers:  map[string]*runningTextBuffer{},
	}
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
	body := r.formatKnownEvent(event, values)
	if body == "" && knownEventType(event.EventType) {
		return ""
	}
	if body == "" {
		body = r.formatGenericEvent(event, values)
	}
	if body == "" {
		return ""
	}
	line := r.composeLine(event, body)
	if event.Status == streampkg.StatusRunning {
		line += " " + r.dim(r.nextFrame())
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

// composeLine renders the icon, layer label, identifier prefix, and body for
// one event. body must already include any inline trailing key=value pairs.
func (r *Renderer) composeLine(event streampkg.Event, body string) string {
	parts := []string{r.statusIcon(event.Status), r.layerHeader(event)}
	if body != "" {
		parts = append(parts, body)
	}
	return strings.Join(parts, " ")
}

func (r *Renderer) layerHeader(event streampkg.Event) string {
	layer, label := layerName(event.From.Layer)
	id := layerIdentifier(event)
	if id == "" {
		return r.colorLayer(layer, label)
	}
	header := fmt.Sprintf("%s%s%s%s", label, r.dim("["), r.colorIdent(id), r.dim("]"))
	return r.colorLayer(layer, header)
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
		return r.formatRunnerPhase(event, values)
	case streampkg.EventRunnerNodeStarted, streampkg.EventRunnerNodeCompleted, streampkg.EventRunnerNodeFailed:
		return r.formatNodeEvent(event, values, strings.TrimPrefix(event.EventType, "runner."))
	case streampkg.EventExecutorPolishStarted, streampkg.EventExecutorPolishCompleted, streampkg.EventExecutorPolishFailed,
		streampkg.EventExecutorExecuteStarted, streampkg.EventExecutorExecuteCompleted, streampkg.EventExecutorExecuteFailed,
		streampkg.EventExecutorReportStarted, streampkg.EventExecutorReportCompleted, streampkg.EventExecutorReportFailed:
		return r.formatNodeEvent(event, values, strings.TrimPrefix(event.EventType, "executor."))
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

func (r *Renderer) formatTextDelta(event streampkg.Event, kind string) string {
	text, ok := deltaString(event)
	if !ok || strings.TrimSpace(text) == "" {
		return ""
	}
	if exchangeText(text) {
		return fmt.Sprintf("%s %s exchange=%s", r.tag(kind), r.dim("·"), r.exchangeReference(event, text))
	}
	if event.Status == streampkg.StatusRunning && kind == "reasoning" {
		return r.formatRunningReasoning(event, text)
	}
	if event.Status == streampkg.StatusRunning && kind == "output" {
		return r.formatRunningOutput(event, kind)
	}
	if event.From.Layer == "orchestrator" && stringValue(event.Metadata["stage"]) == "planning" && kind == "output" {
		return fmt.Sprintf("planning output captured %s", r.kv("status", string(event.Status)))
	}
	if event.From.Layer == "orchestrator" && kind == "output" {
		trimmed := strings.TrimSpace(text)
		if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(trimmed)) {
			return ""
		}
	}
	clean, truncated := r.compact(text)
	line := fmt.Sprintf("%s %s %s", r.tag(kind), r.dim("·"), clean)
	if truncated {
		line += " " + r.kv("truncated", "true")
	}
	return line
}

func (r *Renderer) formatRunningOutput(event streampkg.Event, kind string) string {
	key := streamDeltaKey(event, kind)
	if r.runningOutputSeen[key] {
		return ""
	}
	r.runningOutputSeen[key] = true
	return fmt.Sprintf("%s %s streaming", r.tag(kind), r.dim("·"))
}

func (r *Renderer) formatRunningReasoning(event streampkg.Event, text string) string {
	key := streamDeltaKey(event, "reasoning")
	buf := r.reasoningBuffers[key]
	if buf == nil {
		buf = &runningTextBuffer{}
		r.reasoningBuffers[key] = buf
	}
	buf.text += text
	if len(buf.text)-buf.emitted < reasoningFlushBytes && !endsReasoningPhrase(text) {
		return ""
	}
	chunk := strings.TrimSpace(buf.text[buf.emitted:])
	buf.emitted = len(buf.text)
	if chunk == "" {
		return ""
	}
	clean, truncated := r.compact(chunk)
	line := fmt.Sprintf("%s %s %s", r.tag("reasoning"), r.dim("·"), clean)
	if truncated {
		line += " " + r.kv("truncated", "true")
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
	parts := []string{}
	if message != "" {
		parts = append(parts, message)
	}
	parts = append(parts, r.kv("status", string(event.Status)), r.kv("event", event.EventType))
	if runnerID := stringValue(values["runner_id"]); runnerID != "" {
		parts = append(parts, r.kv("runner_id", runnerID))
	}
	if truncated {
		parts = append(parts, r.kv("truncated", "true"))
	}
	return strings.Join(parts, " ")
}

func (r *Renderer) formatRunnerPhase(event streampkg.Event, values map[string]any) string {
	phase := stringValue(values["phase"])
	if phase == "" {
		phase = "unknown"
	}
	status := stringValue(values["status"])
	if status == "" {
		status = string(event.Status)
	}
	return r.kv("phase", phase) + " " + r.kv("status", status)
}

func (r *Renderer) formatNodeEvent(event streampkg.Event, values map[string]any, eventName string) string {
	parts := []string{
		r.kv("event", eventName),
		r.kv("status", string(event.Status)),
	}
	if node := nodeID(event, values); node != "" {
		parts = append(parts, r.kv("node", node))
	}
	if skill := skillName(event); skill != "" && skill != "unknown" {
		parts = append(parts, r.kv("skill", skill))
	}
	if errText := stringValue(values["error"]); errText != "" {
		errText, _ = r.compact(errText)
		parts = append(parts, r.kvQuoted("error", errText))
	}
	return strings.Join(parts, " ")
}

func (r *Renderer) formatSkillMemo(event streampkg.Event, values map[string]any) string {
	memo, truncated := r.compact(stringValue(values["memo"]))
	if memo == "" {
		return ""
	}
	parts := []string{
		r.tag("memo"), r.dim("·"), memo,
		r.kv("status", string(event.Status)),
	}
	if node := nodeID(event, values); node != "" {
		parts = append(parts, r.kv("node", node))
	}
	if skill := skillName(event); skill != "" && skill != "unknown" {
		parts = append(parts, r.kv("skill", skill))
	}
	if stage := stringValue(values["stage"]); stage != "" {
		parts = append(parts, r.kv("stage", stage))
	}
	if truncated || boolValue(values["truncated"]) {
		parts = append(parts, r.kv("truncated", "true"))
	}
	return strings.Join(parts, " ")
}

func (r *Renderer) formatArtifact(event streampkg.Event, values map[string]any) string {
	path := stringValue(values["path"])
	if path == "" {
		return ""
	}
	parts := []string{r.kv("artifact", fileURL(path))}
	if resourceType := stringValue(values["resource_type"]); resourceType != "" {
		parts = append(parts, r.kv("type", resourceType))
	}
	if stage := stringValue(values["stage"]); stage != "" {
		parts = append(parts, r.kv("stage", stage))
	}
	if desc := stringValue(values["description"]); desc != "" {
		desc, _ = r.compact(desc)
		parts = append(parts, r.kvQuoted("description", desc))
	}
	line := strings.Join(parts, " ")
	if inline := r.inlineImage(path); inline != "" {
		line += "\n" + inline
	}
	return line
}

func (r *Renderer) formatToolCall(event streampkg.Event, values map[string]any) string {
	parts := []string{
		r.kv("event", strings.TrimPrefix(event.EventType, "llm.")),
		r.kv("status", string(event.Status)),
		r.kv("skill", skillName(event)),
		r.kv("tool", stringValue(values["name"])),
		r.kv("call", stringValue(values["call_id"])),
	}
	if args := stringValue(values["arguments"]); args != "" {
		args, _ = r.compact(args)
		parts = append(parts, r.kvQuoted("args", args))
	}
	return strings.Join(parts, " ")
}

func (r *Renderer) formatToolExecution(event streampkg.Event, values map[string]any) string {
	parts := []string{
		r.kv("event", strings.TrimPrefix(event.EventType, "tool.")),
		r.kv("status", string(event.Status)),
		r.kv("skill", skillName(event)),
		r.kv("tool", stringValue(values["name"])),
		r.kv("call", stringValue(values["call_id"])),
	}
	if errText := stringValue(values["error"]); errText != "" {
		errText, _ = r.compact(errText)
		parts = append(parts, r.kvQuoted("error", errText))
	}
	return strings.Join(parts, " ")
}

func (r *Renderer) formatToolResult(event streampkg.Event, values map[string]any) string {
	parts := []string{
		r.kv("event", "tool_result"),
		r.kv("status", string(event.Status)),
		r.kv("skill", skillName(event)),
		r.kv("tool", stringValue(values["name"])),
		r.kv("call", stringValue(values["call_id"])),
	}
	if errText := stringValue(values["error"]); errText != "" {
		errText, _ = r.compact(errText)
		parts = append(parts, r.kvQuoted("error", errText))
	}
	return strings.Join(parts, " ")
}

func (r *Renderer) formatGenericEvent(event streampkg.Event, values map[string]any) string {
	if text, ok := deltaString(event); ok {
		text, truncated := r.compact(text)
		parts := []string{
			r.kv("event", event.EventType),
			r.kv("status", string(event.Status)),
			r.kvQuoted("delta", text),
		}
		if truncated {
			parts = append(parts, r.kv("truncated", "true"))
		}
		return strings.Join(parts, " ")
	}
	if len(values) == 0 {
		return ""
	}
	return strings.Join([]string{
		r.kv("event", event.EventType),
		r.kv("status", string(event.Status)),
	}, " ")
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
	return ansi(33, name) // yellow for tag
}

func (r *Renderer) colorKey(key string) string {
	if !r.cfg.Color {
		return key
	}
	return ansi(36, key) // cyan
}

func (r *Renderer) colorIdent(text string) string {
	if !r.cfg.Color {
		return text
	}
	return ansi(33, text) // yellow
}

func (r *Renderer) colorLayer(layer, text string) string {
	if !r.cfg.Color {
		return text
	}
	color := layerColor(layer)
	return ansi(color, text)
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
	return ansi(statusColor(status), text)
}

func (r *Renderer) dim(text string) string {
	if !r.cfg.Color {
		return text
	}
	return ansi(90, text)
}

func ansi(color int, text string) string {
	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", color, text)
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
	case "executor":
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
	case "executor":
		return layer, "Executor"
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
	case "executor":
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

func streamDeltaKey(event streampkg.Event, kind string) string {
	return strings.Join([]string{
		kind,
		event.From.Layer,
		event.From.ID,
		event.From.ParentID,
		event.Scope.RequestID,
		event.Scope.RunnerID,
		event.Scope.NodeID,
	}, "|")
}

func endsReasoningPhrase(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasSuffix(trimmed, ".") ||
		strings.HasSuffix(trimmed, "?") ||
		strings.HasSuffix(trimmed, "!") ||
		strings.HasSuffix(trimmed, "。") ||
		strings.HasSuffix(trimmed, "？") ||
		strings.HasSuffix(trimmed, "！") ||
		strings.Contains(text, "\n")
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
	return runnerpkg.LooksLikeExchangeMarkdown(text)
}
