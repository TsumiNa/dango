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
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

const (
	defaultMaxText           = 1200
	defaultMaxLineWidth      = 100
	defaultRunningSoftLimit  = 1200
	defaultRunnerIDClip      = 8
	defaultProgressTick      = 120 * time.Millisecond
	minMarqueeWindow         = 16
	approxCharsPerToken      = 4
	completionMarqueeMinChar = 80
	// terminalWidthSafetyMargin reserves a couple of cells so a composed line
	// stays clear of the terminal's right edge. Without it terminals on
	// boundary widths (e.g. fish prompt indicators) can wrap a marquee line and
	// break the in-place \r\x1b[K update mechanism.
	terminalWidthSafetyMargin = 2
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

	// Debug surfaces internal identifiers (node, call_id, runner_id) and other
	// low-level fields that are normally hidden because they don't help end
	// users. Layer headers like Executor[node-id] still appear without Debug.
	Debug bool

	// MaxText caps long delta fields. Zero uses a conservative default
	// (~200 words). Static reasoning/output text longer than this is truncated
	// with an ellipsis.
	MaxText int

	// MaxLineWidth caps final terminal lines using ANSI-aware cell width. Zero
	// uses a conservative default suitable for ordinary terminal windows.
	MaxLineWidth int

	// RunningSoftLimit is the character budget below which a streaming
	// reasoning/output buffer is shown verbatim on its live line. Once the
	// accumulated text grows past this limit, the renderer attaches a
	// "(Tokens N)" counter and replaces the line with a completion summary
	// when the live source closes. Zero uses the same default as MaxText.
	RunningSoftLimit int

	// ProgressFrames are appended to running events to make repeated progress
	// visibly alive. Nil uses ASCII spinner frames.
	ProgressFrames []string

	// ProgressTick controls how often an active terminal line advances its
	// spinner while no new stream event has arrived. Zero uses the default tick.
	ProgressTick time.Duration

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
		},
	}
}

// Renderer writes compact terminal lines for stream events.
type Renderer struct {
	out            io.Writer
	cfg            Config
	lip            *lipgloss.Renderer
	lastLine       string
	frame          int
	liveLineActive bool
	liveLineKey    string
	liveLineBase   string
	textBuffers    map[string]*runningTextBuffer
}

// runningTextBuffer accumulates streaming reasoning/output deltas so the
// renderer can show a single rolling line and emit a stats summary once the
// live source closes.
type runningTextBuffer struct {
	text      string
	startedAt time.Time
	source    streampkg.Source
	scope     streampkg.Scope
	metadata  map[string]any
	kind      string
	marqueed  bool
}

// New creates a Renderer that writes to out. A nil writer discards output.
func New(out io.Writer, cfg Config) *Renderer {
	if out == nil {
		out = io.Discard
	}
	if cfg.MaxText <= 0 {
		cfg.MaxText = defaultMaxText
	}
	if cfg.MaxLineWidth <= 0 {
		cfg.MaxLineWidth = defaultMaxLineWidth
	}
	// Reserve a couple of cells inside the caller's reported width so the
	// composed line always lands strictly inside the terminal. Crossing the
	// right edge wraps the line and breaks our "\r\x1b[K" in-place update.
	if cfg.MaxLineWidth > terminalWidthSafetyMargin*2 {
		cfg.MaxLineWidth -= terminalWidthSafetyMargin
	}
	if cfg.RunningSoftLimit <= 0 {
		cfg.RunningSoftLimit = cfg.MaxText
	}
	if cfg.ProgressFrames == nil {
		cfg.ProgressFrames = []string{"|", "/", "-", "\\"}
	}
	if cfg.ProgressTick <= 0 {
		cfg.ProgressTick = defaultProgressTick
	}
	if cfg.ImageMaxBytes <= 0 {
		cfg.ImageMaxBytes = 2 << 20
	}
	r := &Renderer{
		out:         out,
		cfg:         cfg,
		textBuffers: map[string]*runningTextBuffer{},
	}
	r.lip = lipgloss.NewRenderer(out)
	if cfg.Color {
		// Force ANSI colors regardless of TTY detection so callers that
		// explicitly opt in (after their own io.Writer/TTY check) always get
		// styled output.
		r.lip.SetColorProfile(termenv.ANSI256)
	} else {
		r.lip.SetColorProfile(termenv.Ascii)
	}
	return r
}

// RenderSubscription drains sub until it closes or ctx is canceled.
func (r *Renderer) RenderSubscription(ctx context.Context, sub *streampkg.Subscription) error {
	return r.RenderSubscriptionObserved(ctx, sub, nil)
}

// RenderSubscriptionObserved drains sub and calls observe for each event
// before rendering it. It renders exactly the events delivered by sub, so
// callers that want logical event rendering should pass a subscription using
// the default expanded delivery rather than a raw merge-bundle stream.
// Callers that also want raw debug traffic should observe a separate
// subscription created with [streampkg.WithRawStream]. Passing a raw
// subscription here causes raw frames to be rendered.
func (r *Renderer) RenderSubscriptionObserved(ctx context.Context, sub *streampkg.Subscription, observe func(streampkg.Event) error) error {
	if sub == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(r.cfg.ProgressTick)
	defer ticker.Stop()
	for {
		select {
		case event, ok := <-sub.Events():
			if !ok {
				return r.finishLiveLine()
			}
			if observe != nil {
				if err := observe(event); err != nil {
					return err
				}
			}
			if err := r.RenderEvent(event); err != nil {
				return err
			}
		case <-ticker.C:
			if err := r.refreshLiveLine(); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// RenderEvent formats event and writes one or more terminal lines.
func (r *Renderer) RenderEvent(event streampkg.Event) error {
	line := r.formatEvent(event, false)
	if r.isLiveEvent(event) {
		if line == "" {
			return nil
		}
		return r.renderLiveLine(liveLineKey(event), line)
	}
	if err := r.finishLiveLine(); err != nil {
		return err
	}
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
	return r.formatEvent(event, true)
}

func (r *Renderer) formatEvent(event streampkg.Event, includeFrame bool) string {
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
	if includeFrame && r.isLiveEvent(event) {
		line = r.lineWithFrame(line)
	}
	return line
}

func (r *Renderer) isLiveEvent(event streampkg.Event) bool {
	if event.Status != streampkg.StatusRunning {
		return false
	}
	switch event.EventType {
	case streampkg.EventLLMReasoningDelta, streampkg.EventLLMOutputDelta,
		streampkg.EventToolExecutionStarted:
		return true
	default:
		return false
	}
}

func (r *Renderer) renderLiveLine(key string, base string) error {
	r.liveLineActive = true
	r.liveLineKey = key
	r.liveLineBase = base
	line := r.lineWithFrame(base)
	if r.cfg.DedupeRepeated && line == r.lastLine {
		return nil
	}
	_, err := fmt.Fprint(r.out, "\r\x1b[K", line)
	if err == nil {
		r.lastLine = line
	}
	return err
}

func (r *Renderer) refreshLiveLine() error {
	if !r.liveLineActive || r.liveLineBase == "" {
		return nil
	}
	line := r.lineWithFrame(r.liveLineBase)
	_, err := fmt.Fprint(r.out, "\r\x1b[K", line)
	if err == nil {
		r.lastLine = line
	}
	return err
}

func (r *Renderer) finishLiveLine() error {
	if !r.liveLineActive {
		return nil
	}
	r.liveLineActive = false
	r.liveLineKey = ""
	r.liveLineBase = ""
	if _, err := fmt.Fprint(r.out, "\r\x1b[K"); err != nil {
		return err
	}
	return r.flushMarqueedBuffers()
}

// flushMarqueedBuffers emits a completion summary for every reasoning/output
// buffer that grew past the soft limit, then drops them from the buffer map.
// Buffers that never reached marquee mode are kept so an interrupted source
// can resume contributing to the same accumulating text on its next delta.
func (r *Renderer) flushMarqueedBuffers() error {
	if len(r.textBuffers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.textBuffers))
	for k, buf := range r.textBuffers {
		if buf != nil && buf.marqueed {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	// Stable order so multi-buffer flushes don't shuffle between runs.
	sort.Strings(keys)
	for _, k := range keys {
		buf := r.textBuffers[k]
		delete(r.textBuffers, k)
		summary := r.buildRunningSummary(buf)
		if summary == "" {
			continue
		}
		if _, err := fmt.Fprintln(r.out, summary); err != nil {
			return err
		}
		r.lastLine = summary
	}
	return nil
}

func (r *Renderer) lineWithFrame(line string) string {
	frame := r.nextFrame()
	if frame == "" {
		return r.fitLine(line)
	}
	return r.fitLine(line + " " + r.dim(frame))
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
	if event.Status == streampkg.StatusRunning {
		return r.formatRunningText(event, kind, text)
	}
	if isValidExchangeDocMarkdown(text) {
		return fmt.Sprintf("%s %s %s=%s", r.tag(kind), r.dim("·"), r.colorKey("exchange"), r.colorPath(r.exchangeReference(event, text)))
	}
	if isValidHandoffDocMarkdown(text) {
		return fmt.Sprintf("%s %s handoff markdown captured %s", r.tag(kind), r.dim("·"), r.kv("bytes", fmt.Sprint(len(text))))
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
		line += " " + r.dim(fmt.Sprintf("(Tokens %d)", estimateTokens(text)))
	}
	return line
}

// formatRunningText composes a single-line live update for a streaming
// reasoning/output buffer. The line shows the most recent characters of the
// accumulated text and, once the buffer grows past the soft limit, attaches a
// "(Tokens N)" counter so the user keeps a sense of how much output has been
// produced without the line itself getting any longer.
func (r *Renderer) formatRunningText(event streampkg.Event, kind string, text string) string {
	key := streamDeltaKey(event, kind)
	buf := r.textBuffers[key]
	if buf == nil {
		buf = &runningTextBuffer{
			startedAt: time.Now(),
			source:    event.From,
			scope:     event.Scope,
			metadata:  cloneMetadata(event.Metadata),
			kind:      kind,
		}
		r.textBuffers[key] = buf
	}
	buf.text += text
	chunk := strings.TrimSpace(buf.text)
	if chunk == "" {
		return ""
	}
	if looksLikeExchangeDraft(chunk) {
		return fmt.Sprintf("%s %s drafting exchange %s", r.tag(kind), r.dim("·"), r.kv("bytes", fmt.Sprint(len(buf.text))))
	}
	if looksLikeHandoffDraft(chunk) {
		return fmt.Sprintf("%s %s drafting handoff %s", r.tag(kind), r.dim("·"), r.kv("bytes", fmt.Sprint(len(buf.text))))
	}
	cleaned := compactWhitespace(chunk)
	cleanedWidth := ansi.StringWidth(cleaned)
	prefix := fmt.Sprintf("%s %s ", r.tag(kind), r.dim("·"))
	prefixWidth := ansi.StringWidth(prefix)
	headerWidth := r.runningHeaderWidth(event)
	frameWidth := r.frameWidth()

	showCounter := cleanedWidth > r.cfg.RunningSoftLimit
	counter := ""
	counterWidth := 0
	if showCounter {
		buf.marqueed = true
		counter = " " + r.dim(fmt.Sprintf("(Tokens %d)", estimateTokens(cleaned)))
		counterWidth = ansi.StringWidth(counter)
	}

	available := r.cfg.MaxLineWidth - headerWidth - prefixWidth - counterWidth - frameWidth
	if available < minMarqueeWindow {
		available = minMarqueeWindow
	}
	visible := cleaned
	if cleanedWidth > available {
		visible = ansi.TruncateLeft(cleaned, cleanedWidth-available, "")
	}
	return prefix + visible + counter
}

// buildRunningSummary returns the line shown in place of a freshly-cleared
// marquee live line so the user keeps a record of what just streamed past.
func (r *Renderer) buildRunningSummary(buf *runningTextBuffer) string {
	if buf == nil {
		return ""
	}
	cleaned := compactWhitespace(buf.text)
	chars := ansi.StringWidth(cleaned)
	tokens := estimateTokens(cleaned)
	parts := []string{
		r.tag(buf.kind + " complete"), r.dim("·"),
		r.kv("tokens", fmt.Sprintf("~%d", tokens)),
		r.kv("chars", fmt.Sprint(chars)),
	}
	if !buf.startedAt.IsZero() {
		dur := time.Since(buf.startedAt).Round(100 * time.Millisecond)
		parts = append(parts, r.kv("dur", dur.String()))
	}
	body := strings.Join(parts, " ")
	event := streampkg.Event{
		EventType: buf.kind + ".complete",
		From:      buf.source,
		Status:    streampkg.StatusCompleted,
		Scope:     buf.scope,
		Metadata:  buf.metadata,
	}
	return r.composeLine(event, body)
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
	if r.cfg.Debug {
		if runnerID := stringValue(values["runner_id"]); runnerID != "" {
			parts = append(parts, r.kv("runner_id", runnerID))
		}
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
	// node= duplicates Executor[node-id] for executor-layer events, so we keep
	// it only for runner-layer events (which header by runner ID) or in debug.
	if node := nodeID(event, values); node != "" && (r.cfg.Debug || event.From.Layer == "runner") {
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
	if r.cfg.Debug {
		if node := nodeID(event, values); node != "" {
			parts = append(parts, r.kv("node", node))
		}
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
	parts := []string{r.colorKey("artifact") + "=" + r.colorPath(fileURL(path))}
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
	}
	if r.cfg.Debug {
		parts = append(parts, r.kv("call", stringValue(values["call_id"])))
	}
	if args := stringValue(values["arguments"]); args != "" {
		args, _ = r.compact(args)
		parts = append(parts, r.kvQuoted("args", args))
	}
	return strings.Join(parts, " ")
}

func (r *Renderer) formatToolExecution(event streampkg.Event, values map[string]any) string {
	name := stringValue(values["name"])
	if name == "" {
		name = "unknown"
	}
	if event.EventType == streampkg.EventToolExecutionStarted {
		return fmt.Sprintf("%s %s %s", r.tag("tool calling"), r.dim("·"), name)
	}
	label := "tool completed"
	if event.EventType == streampkg.EventToolExecutionFailed {
		label = "tool failed"
	}
	parts := []string{
		r.tag(label), r.dim("·"), name,
		r.kv("status", string(event.Status)),
		r.kv("skill", skillName(event)),
	}
	if r.cfg.Debug {
		parts = append(parts, r.kv("call", stringValue(values["call_id"])))
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
	}
	if r.cfg.Debug {
		parts = append(parts, r.kv("call", stringValue(values["call_id"])))
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
	if ansi.StringWidth(text) <= r.cfg.MaxText {
		return text, false
	}
	return ansi.Truncate(text, r.cfg.MaxText, "..."), true
}

func (r *Renderer) fitLine(line string) string {
	if r.cfg.MaxLineWidth <= 0 || ansi.StringWidth(line) <= r.cfg.MaxLineWidth {
		return line
	}
	return ansi.Truncate(line, r.cfg.MaxLineWidth, "...")
}

func (r *Renderer) nextFrame() string {
	if len(r.cfg.ProgressFrames) == 0 {
		return ""
	}
	frame := r.cfg.ProgressFrames[r.frame%len(r.cfg.ProgressFrames)]
	r.frame++
	return frame
}

func (r *Renderer) frameWidth() int {
	if len(r.cfg.ProgressFrames) == 0 {
		return 0
	}
	width := 0
	for _, frame := range r.cfg.ProgressFrames {
		if w := ansi.StringWidth(frame); w > width {
			width = w
		}
	}
	return width + 1 // include the leading space
}

// runningHeaderWidth approximates the cell width of the icon + " " + layer
// header that composeLine will prepend, so the marquee body can stay within
// MaxLineWidth without further truncation by fitLine cutting the counter.
func (r *Renderer) runningHeaderWidth(event streampkg.Event) int {
	icon := r.statusIcon(event.Status)
	header := r.layerHeader(event)
	return ansi.StringWidth(icon) + 1 + ansi.StringWidth(header) + 1
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

func liveLineKey(event streampkg.Event) string {
	if event.EventType == streampkg.EventLLMReasoningDelta || event.EventType == streampkg.EventLLMOutputDelta {
		return streamDeltaKey(event, kindForEvent(event.EventType))
	}
	return strings.Join([]string{
		event.EventType,
		event.From.Layer,
		event.From.ID,
		event.From.ParentID,
		event.Scope.RequestID,
		event.Scope.RunnerID,
		event.Scope.NodeID,
	}, "|")
}

func kindForEvent(eventType string) string {
	switch eventType {
	case streampkg.EventLLMReasoningDelta:
		return "reasoning"
	case streampkg.EventLLMOutputDelta:
		return "output"
	default:
		return ""
	}
}

func cloneMetadata(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(meta))
	for k, v := range meta {
		out[k] = v
	}
	return out
}

func compactWhitespace(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// estimateTokens approximates the OpenAI-style token count for text using the
// "~4 characters per token" rule of thumb. It exists so the live counter and
// completion summary can give the user a rough sense of throughput without
// pulling in a real tokenizer.
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	chars := ansi.StringWidth(text)
	tokens := chars / approxCharsPerToken
	if tokens == 0 {
		tokens = 1
	}
	return tokens
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

func isValidExchangeDocMarkdown(text string) bool {
	_, err := runnerpkg.ParseExchangeDocMarkdown(text)
	return err == nil
}

func looksLikeExchangeDraft(text string) bool {
	return looksLikeChannelDraft(text, streampkg.ChannelKindExchange)
}

func looksLikeHandoffDraft(text string) bool {
	return looksLikeChannelDraft(text, streampkg.ChannelKindHandoff)
}

func looksLikeChannelDraft(text string, kind streampkg.ChannelKind) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "---\n") {
		return false
	}
	frontMatter := trimmed[len("---\n"):]
	for _, marker := range []string{"\n---\n", "\n---\r\n", "\n---"} {
		if idx := strings.Index(frontMatter, marker); idx >= 0 {
			frontMatter = frontMatter[:idx]
			break
		}
	}
	for _, line := range strings.Split(frontMatter, "\n") {
		if strings.TrimSpace(line) == "kind: "+string(kind) {
			return true
		}
	}
	return false
}

func isValidHandoffDocMarkdown(text string) bool {
	_, err := runnerpkg.ParseHandoffMarkdown(text)
	return err == nil
}
