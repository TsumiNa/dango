package streamrender

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	streampkg "github.com/tsumina/dango/stream"
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
	// users. Layer headers like Agent[node-id] still appear without Debug.
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
// surfaces orchestrator planning, runner phases, agent stages, skill memos,
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
