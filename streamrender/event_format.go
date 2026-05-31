package streamrender

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	runnerpkg "github.com/tsumina/dango/engine/runner"
	streampkg "github.com/tsumina/dango/stream"
)

// formatEvent returns the terminal line for event, or an empty string when
// the event is filtered or intentionally silent. includeFrame=false drops the
// leading provenance frame and is used by the live-rendering path that already
// owns its own framing.
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

func knownEventType(eventType string) bool {
	switch eventType {
	case streampkg.EventLLMReasoningDelta, streampkg.EventLLMOutputDelta,
		streampkg.EventStatusStarted, streampkg.EventStatusProgress, streampkg.EventStatusCompleted, streampkg.EventStatusFailed,
		streampkg.EventRunnerPhaseChanged,
		streampkg.EventRunnerNodeStarted, streampkg.EventRunnerNodeCompleted, streampkg.EventRunnerNodeFailed,
		streampkg.EventAgentPolishStarted, streampkg.EventAgentPolishCompleted, streampkg.EventAgentPolishFailed,
		streampkg.EventAgentExecuteStarted, streampkg.EventAgentExecuteCompleted, streampkg.EventAgentExecuteFailed,
		streampkg.EventAgentReportStarted, streampkg.EventAgentReportCompleted, streampkg.EventAgentReportFailed,
		streampkg.EventExchangePublished,
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
	case streampkg.EventAgentPolishStarted, streampkg.EventAgentPolishCompleted, streampkg.EventAgentPolishFailed,
		streampkg.EventAgentExecuteStarted, streampkg.EventAgentExecuteCompleted, streampkg.EventAgentExecuteFailed,
		streampkg.EventAgentReportStarted, streampkg.EventAgentReportCompleted, streampkg.EventAgentReportFailed:
		return r.formatNodeEvent(event, values, strings.TrimPrefix(event.EventType, "agent."))
	case streampkg.EventExchangePublished:
		return r.formatExchangePublished(values)
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
		if ref, ok := r.exchangeReference(event, text); ok {
			return fmt.Sprintf("%s %s %s=%s", r.tag(kind), r.dim("·"), r.colorKey("exchange"), r.colorPath(ref))
		}
		return fmt.Sprintf("%s %s exchange markdown captured %s", r.tag(kind), r.dim("·"), r.kv("bytes", fmt.Sprint(len(text))))
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
	// node= duplicates Agent[node-id] for agent-layer events, so we keep
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

func (r *Renderer) formatExchangePublished(values map[string]any) string {
	parts := []string{}
	if path := stringValue(values["path"]); path != "" {
		parts = append(parts, r.colorKey("exchange")+"="+r.colorPath(fileURL(path)))
	} else {
		parts = append(parts, "exchange published")
	}
	if title := stringValue(values["title"]); title != "" {
		parts = append(parts, r.kvQuoted("title", title))
	}
	return strings.Join(parts, " ")
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

// runningHeaderWidth approximates the cell width of the icon + " " + layer
// header that composeLine will prepend, so the marquee body can stay within
// MaxLineWidth without further truncation by fitLine cutting the counter.
func (r *Renderer) runningHeaderWidth(event streampkg.Event) int {
	icon := r.statusIcon(event.Status)
	header := r.layerHeader(event)
	return ansi.StringWidth(icon) + 1 + ansi.StringWidth(header) + 1
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

func (r *Renderer) exchangeReference(event streampkg.Event, text string) (string, bool) {
	if r.cfg.ExchangeDir == "" {
		return "", false
	}
	if err := os.MkdirAll(r.cfg.ExchangeDir, 0o755); err != nil {
		return fmt.Sprintf("write-error:%s", err), true
	}
	path := filepath.Join(r.cfg.ExchangeDir, fmt.Sprintf("exchange-%012d.md", event.SequenceNumber))
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Sprintf("write-error:%s", err), true
	}
	return fileURL(path), true
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
