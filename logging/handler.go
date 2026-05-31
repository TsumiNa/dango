package logging

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// modulePrefix is stripped from source paths so emitted lines stay
// readable. The constant intentionally hard-codes the dango module
// path; if the module is ever renamed this needs to follow.
const modulePrefix = "github.com/tsumina/dango/"

var bufferPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// prettyHandler emits dango's compact single-line slog format:
// "HH:MM:SS.mmm  LVL  pkg/file.go:NN  message  k=v ...".
//
// Color is decided once at construction by inspecting the writer and
// forced onto a writer-bound lipgloss.Renderer so styling targets the
// detected sink rather than the package-global stdout profile. The
// underlying writer is protected by a mutex shared across all derived
// handlers so concurrent emits do not interleave.
type prettyHandler struct {
	mu        *sync.Mutex
	w         io.Writer
	level     slog.Leveler
	addSource bool
	attrs     []slog.Attr
	groups    []string
	styles    levelStyles
}

type levelStyles struct {
	dbg lipgloss.Style
	inf lipgloss.Style
	wrn lipgloss.Style
	err lipgloss.Style
	src lipgloss.Style
	key lipgloss.Style
}

func newLevelStyles(r *lipgloss.Renderer) levelStyles {
	return levelStyles{
		dbg: r.NewStyle().Foreground(lipgloss.Color("245")),
		inf: r.NewStyle().Foreground(lipgloss.Color("39")),
		wrn: r.NewStyle().Foreground(lipgloss.Color("214")),
		err: r.NewStyle().Foreground(lipgloss.Color("196")).Bold(true),
		src: r.NewStyle().Foreground(lipgloss.Color("245")),
		key: r.NewStyle().Foreground(lipgloss.Color("245")),
	}
}

// newPrettyHandler constructs the dango pretty handler over w. Color
// is auto-enabled only when w is a TTY-backed *os.File; non-file
// writers (buffers, pipes, regular files) always receive plain text.
// The lipgloss renderer is bound to w (not the package-global one) so
// styling decisions follow the writer that detectColor inspected.
func newPrettyHandler(w io.Writer, level slog.Leveler, addSource bool) slog.Handler {
	renderer := lipgloss.NewRenderer(w)
	if detectColor(w) {
		renderer.SetColorProfile(termenv.ANSI256)
	} else {
		renderer.SetColorProfile(termenv.Ascii)
	}
	return &prettyHandler{
		mu:        &sync.Mutex{},
		w:         w,
		level:     level,
		addSource: addSource,
		styles:    newLevelStyles(renderer),
	}
}

func detectColor(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// Enabled reports whether the handler should process records at l.
func (h *prettyHandler) Enabled(_ context.Context, l slog.Level) bool {
	min := slog.LevelInfo
	if h.level != nil {
		min = h.level.Level()
	}
	return l >= min
}

// Handle formats r and writes the resulting line under the shared
// writer mutex.
func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	buf := bufferPool.Get().(*bytes.Buffer)
	defer func() {
		buf.Reset()
		bufferPool.Put(buf)
	}()

	buf.WriteString(r.Time.Format("15:04:05.000"))
	buf.WriteString("  ")

	buf.WriteString(h.styleLevel(r.Level).Render(levelToken(r.Level)))
	buf.WriteString("  ")

	if h.addSource && r.PC != 0 {
		buf.WriteString(h.styles.src.Render(formatSource(r.PC)))
		buf.WriteString("  ")
	}

	buf.WriteString(escapeNewlines(r.Message))

	for _, a := range h.attrs {
		h.writeAttr(buf, "", a)
	}

	groupPrefix := ""
	if len(h.groups) > 0 {
		groupPrefix = strings.Join(h.groups, ".") + "."
	}
	r.Attrs(func(a slog.Attr) bool {
		h.writeAttr(buf, groupPrefix, a)
		return true
	})

	buf.WriteByte('\n')

	h.mu.Lock()
	_, err := h.w.Write(buf.Bytes())
	h.mu.Unlock()
	return err
}

// WithAttrs returns a derived handler that emits attrs on every
// subsequent record. attrs are pre-prefixed with the groups active at
// the time of the call, matching slog's chain semantics.
func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	out := *h
	prefixed := prefixAttrKeys(h.groups, attrs)
	out.attrs = make([]slog.Attr, 0, len(h.attrs)+len(prefixed))
	out.attrs = append(out.attrs, h.attrs...)
	out.attrs = append(out.attrs, prefixed...)
	return &out
}

// WithGroup returns a derived handler that nests subsequent record
// attrs under name. Already-bound attrs from earlier WithAttrs calls
// are not retroactively re-prefixed.
func (h *prettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	out := *h
	out.groups = make([]string, 0, len(h.groups)+1)
	out.groups = append(out.groups, h.groups...)
	out.groups = append(out.groups, name)
	return &out
}

func prefixAttrKeys(groups []string, attrs []slog.Attr) []slog.Attr {
	if len(groups) == 0 {
		return append([]slog.Attr(nil), attrs...)
	}
	prefix := strings.Join(groups, ".") + "."
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = slog.Attr{Key: prefix + a.Key, Value: a.Value}
	}
	return out
}

func (h *prettyHandler) writeAttr(buf *bytes.Buffer, groupPrefix string, a slog.Attr) {
	// Resolve LogValuer before inspecting Kind so custom types that
	// implement slog.LogValuer (including ones that resolve to groups)
	// are expanded as the slog handler contract requires.
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		sub := a.Value.Group()
		nextPrefix := groupPrefix
		if a.Key != "" {
			nextPrefix = groupPrefix + a.Key + "."
		}
		for _, child := range sub {
			h.writeAttr(buf, nextPrefix, child)
		}
		return
	}
	buf.WriteByte(' ')
	key := groupPrefix + a.Key
	buf.WriteString(h.styles.key.Render(key + "="))
	buf.WriteString(quoteValue(a.Value.String()))
}

func quoteValue(v string) string {
	if needsQuote(v) {
		return strconv.Quote(v)
	}
	return v
}

// escapeNewlines replaces CR/LF in s with their backslash-escaped form
// so a multi-line message stays one physical log line. Without this,
// callers logging multi-line strings (errors, model output) would split
// one record into several prefix-less lines and break the documented
// single-line invariant.
func escapeNewlines(s string) string {
	if !strings.ContainsAny(s, "\n\r") {
		return s
	}
	return strings.NewReplacer("\n", `\n`, "\r", `\r`).Replace(s)
}

func needsQuote(v string) bool {
	if v == "" {
		return true
	}
	for _, r := range v {
		if r == ' ' || r == '=' || r == '"' || r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func levelToken(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "DBG"
	case l < slog.LevelWarn:
		return "INF"
	case l < slog.LevelError:
		return "WRN"
	default:
		return "ERR"
	}
}

func (h *prettyHandler) styleLevel(l slog.Level) lipgloss.Style {
	switch {
	case l < slog.LevelInfo:
		return h.styles.dbg
	case l < slog.LevelWarn:
		return h.styles.inf
	case l < slog.LevelError:
		return h.styles.wrn
	default:
		return h.styles.err
	}
}

func formatSource(pc uintptr) string {
	frames := runtime.CallersFrames([]uintptr{pc})
	frame, _ := frames.Next()
	if frame.File == "" {
		return "?:0"
	}
	return trimSourcePath(frame.File) + ":" + strconv.Itoa(frame.Line)
}

func trimSourcePath(file string) string {
	if i := strings.Index(file, modulePrefix); i >= 0 {
		return file[i+len(modulePrefix):]
	}
	if i := strings.LastIndex(file, "/dango/"); i >= 0 {
		return file[i+len("/dango/"):]
	}
	return file
}
