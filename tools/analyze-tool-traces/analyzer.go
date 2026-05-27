// Package main implements the analyze-tool-traces utility.
//
// It consumes the JSON-lines stream-event log dango runners write to
// artifacts/debug/stream_events.jsonl and reports the data shapes the
// post-alpha hardening phase needs: bash command-head distribution,
// captured inner bodies for Turing-complete heads (python -c / bash -c
// / sh -c / awk / make / xargs), per-skill tallies, and curl/wget URL
// frequencies.
//
// The analyzer filters input by event_type ([streampkg.EventLLMToolCallStarted]
// today), not by the audit-category tag introduced in subtask 60a, so
// traces captured before the tag landed remain analyzable. The
// [Report.AuditEvents] counter reports the subset of events that did
// carry the `category: "audit"` tag (see docs/tool-call-audit-schema.md)
// so callers can tell how much of a trace is audit-grade.
//
// The analyzer supersedes the hand-rolled PR C-3 methodology recorded in
// docs/builtin-tools-restructure-plan.md.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

// Report is the structured analysis result. It is what the JSON sidecar
// serializes and what the markdown formatter consumes; keep new fields
// JSON-marshallable.
type Report struct {
	TotalEvents       int            `json:"total_events"`
	AuditEvents       int            `json:"audit_events"`
	ToolCallStarted   int            `json:"tool_call_started"`
	BashCalls         int            `json:"bash_calls"`
	BashHeads         map[string]int `json:"bash_heads"`
	InnerBodies       map[string][]string `json:"inner_bodies"`
	PerSkillTallies   map[string]int `json:"per_skill_tallies"`
	URLsByHost        map[string]int `json:"urls_by_host"`
	CurlCalls         int            `json:"curl_calls"`
	WgetCalls         int            `json:"wget_calls"`
}

// turingCompleteHeads is the set of bash heads whose argument carries an
// inline program body worth capturing. The set is intentionally short:
// these are the heads where a one-line shell call hides arbitrary code.
var turingCompleteHeads = map[string]bool{
	"python":  true,
	"python3": true,
	"bash":    true,
	"sh":      true,
	"awk":     true,
	"make":    true,
	"xargs":   true,
}

// Analyze reads JSON-lines stream events from r and returns the aggregated
// [Report]. The filter is by event_type rather than the audit category
// tag so traces captured before subtask 60a landed (legacy honshu runs,
// PR C-3 corpus, etc.) still produce a useful summary; the [Report]
// reports AuditEvents separately so callers can tell how much of a trace
// is tagged. Lines that fail to parse are counted toward TotalEvents and
// otherwise ignored, mirroring the lenient behavior the hand-rolled
// PR C-3 analysis used so a partial trace still produces a useful
// summary.
func Analyze(r io.Reader) (Report, error) {
	rep := Report{
		BashHeads:       map[string]int{},
		InnerBodies:     map[string][]string{},
		PerSkillTallies: map[string]int{},
		URLsByHost:      map[string]int{},
	}
	// bufio.Reader (not Scanner) so a single overlong line — memo
	// snapshots and exchange events carry unbounded document bodies, far
	// larger than the [bufio.MaxScanTokenSize] cap — neither aborts the
	// analysis nor silently drops events. ReadBytes returns the partial
	// line at io.EOF so the loop handles files that do not end with a
	// newline.
	br := bufio.NewReader(r)
	for {
		raw, err := br.ReadBytes('\n')
		line := bytes.TrimRight(raw, "\n")
		if len(line) > 0 {
			rep.TotalEvents++
			foldLine(line, &rep)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return rep, nil
			}
			return rep, fmt.Errorf("analyze: read line: %w", err)
		}
	}
}

// foldLine parses one JSON-encoded stream event and folds it into rep.
// Unparseable lines are silently dropped; callers learn about the total
// line count from rep.TotalEvents.
func foldLine(line []byte, rep *Report) {
	var ev streampkg.Event
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	if ev.Metadata["category"] == "audit" {
		rep.AuditEvents++
	}
	if ev.EventType != streampkg.EventLLMToolCallStarted {
		return
	}
	rep.ToolCallStarted++

	var payload toolCallPayload
	if err := json.Unmarshal(ev.Delta, &payload); err != nil {
		return
	}
	skill := skillLabel(ev)
	rep.PerSkillTallies[skill]++
	if payload.Name != "bash" {
		return
	}
	rep.BashCalls++

	cmd := bashCommandFromArgs(payload.Arguments)
	head := commandHead(cmd)
	if head == "" {
		return
	}
	rep.BashHeads[head]++

	if turingCompleteHeads[head] {
		if body := captureInnerBody(head, cmd); body != "" {
			rep.InnerBodies[head] = append(rep.InnerBodies[head], body)
		}
	}
	if head == "curl" {
		rep.CurlCalls++
	}
	if head == "wget" {
		rep.WgetCalls++
	}
	if head == "curl" || head == "wget" {
		for _, raw := range extractURLs(cmd) {
			host := urlHost(raw)
			if host != "" {
				rep.URLsByHost[host]++
			}
		}
	}
}

// toolCallPayload mirrors the audit-event delta documented in
// docs/tool-call-audit-schema.md. Only the fields the analyzer reads.
type toolCallPayload struct {
	Name      string `json:"name"`
	CallID    string `json:"call_id"`
	Arguments string `json:"arguments"`
}

// skillLabel picks the best label for "which skill emitted this call?",
// preferring the explicit metadata.skill_name field the runner stamps onto
// node events and falling back to from.id when that is missing.
func skillLabel(ev streampkg.Event) string {
	if name, ok := ev.Metadata["skill_name"].(string); ok && name != "" {
		return name
	}
	if ev.From.ID != "" {
		return ev.From.ID
	}
	return "(unknown)"
}

// bashCommandFromArgs pulls the `command` field out of the bash tool's
// argument JSON. The audit event already truncates arguments at 4096 bytes;
// when the truncation happens mid-string the JSON may not parse, in which
// case we return the raw substring after `"command":"` so command-head
// extraction still produces a useful tally.
func bashCommandFromArgs(args string) string {
	if args == "" {
		return ""
	}
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(args), &parsed); err == nil {
		return parsed.Command
	}
	if idx := strings.Index(args, `"command":"`); idx >= 0 {
		rest := args[idx+len(`"command":"`):]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			return rest
		}
		return rest[:end]
	}
	return ""
}

// commandHead returns the first whitespace-delimited token of cmd that
// looks like a command name, skipping leading `KEY=value` shell variable
// assignments that would otherwise dominate the head distribution. A
// trailing colon or semicolon is stripped so heredoc preambles still
// match. Wrapper prefixes (`sudo`, `time`, `env`, etc.) are intentionally
// not unwrapped — they surface as their own heads in the distribution so
// the report makes "what got invoked under sudo" visible.
func commandHead(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	for _, tok := range strings.Fields(cmd) {
		if isAssignmentToken(tok) {
			continue
		}
		return strings.TrimRight(tok, ":;")
	}
	return ""
}

// isAssignmentToken reports whether tok is a `NAME=value` env-style
// assignment (POSIX: an unquoted token where an `=` appears after one or
// more name-safe characters).
func isAssignmentToken(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := tok[i]
		nameOK := c == '_' ||
			(c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(i > 0 && c >= '0' && c <= '9')
		if !nameOK {
			return false
		}
	}
	return true
}

// captureInnerBody extracts a short snippet of the inline program supplied
// to a Turing-complete head. The exact rule depends on the head:
//
//   - `python -c "BODY"` / `bash -c BODY` / `sh -c BODY` / `awk BODY`:
//     capture the argument that follows the head (and `-c` when present).
//   - `python <<'PY' ... PY`: capture the heredoc body.
//   - `xargs CMD`: capture the wrapped command (the rest of the line).
//   - `make TARGET`: capture the requested target list.
//
// The captured body is trimmed and clipped at 256 bytes so the report
// stays readable; the original event still carries the full argument.
func captureInnerBody(head, cmd string) string {
	cmd = strings.TrimSpace(cmd)
	tokens := strings.Fields(cmd)
	if len(tokens) <= 1 {
		return ""
	}

	switch head {
	case "python", "python3", "bash", "sh":
		if body := afterDashC(cmd); body != "" {
			return clipBody(body)
		}
		if body := heredocBody(cmd); body != "" {
			return clipBody(body)
		}
	case "awk":
		// awk SCRIPT ... or awk -f FILE; only the inline-script form is
		// Turing-complete from the trace's perspective.
		if strings.HasPrefix(tokens[1], "-") {
			return ""
		}
		return clipBody(tokens[1])
	case "make":
		return clipBody(strings.Join(tokens[1:], " "))
	case "xargs":
		// Drop xargs's own flags and capture the wrapped command.
		for i := 1; i < len(tokens); i++ {
			if !strings.HasPrefix(tokens[i], "-") {
				return clipBody(strings.Join(tokens[i:], " "))
			}
		}
	}
	return ""
}

// afterDashC returns the body following a `-c` flag, handling both
// quoted-on-one-line forms and the bare `-c BODY` form. It does not try to
// recover full shell quoting; the goal is a useful summary.
func afterDashC(cmd string) string {
	idx := strings.Index(cmd, " -c ")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(cmd[idx+len(" -c "):])
	if rest == "" {
		return ""
	}
	if strings.HasPrefix(rest, `"`) {
		if end := strings.Index(rest[1:], `"`); end >= 0 {
			return rest[1 : 1+end]
		}
		return rest[1:]
	}
	if strings.HasPrefix(rest, "'") {
		if end := strings.Index(rest[1:], "'"); end >= 0 {
			return rest[1 : 1+end]
		}
		return rest[1:]
	}
	return rest
}

// heredocBody returns the contents of a single `<<TAG ... TAG` heredoc, if
// present. The tag may be quoted or bare. Multiple heredocs in one command
// are uncommon; only the first is returned.
func heredocBody(cmd string) string {
	open := strings.Index(cmd, "<<")
	if open < 0 {
		return ""
	}
	rest := cmd[open+2:]
	if strings.HasPrefix(rest, "-") {
		rest = rest[1:]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return ""
	}
	// Tag runs to the next whitespace; strip quoting.
	tagEnd := strings.IndexAny(rest, " \t\n")
	if tagEnd < 0 {
		return ""
	}
	tag := rest[:tagEnd]
	tag = strings.Trim(tag, `'"`)
	body := rest[tagEnd:]
	close := strings.Index(body, "\n"+tag)
	if close < 0 {
		return strings.TrimSpace(body)
	}
	return strings.TrimSpace(body[:close])
}

func clipBody(s string) string {
	const cap = 256
	s = strings.TrimSpace(s)
	if len(s) <= cap {
		return s
	}
	return s[:cap] + "…"
}

// extractURLs returns http(s) URLs that appear as standalone tokens in
// cmd. The matcher is intentionally simple — a URL ends at the next
// whitespace, semicolon, single quote, or double quote — and trims common
// trailing punctuation. It is not meant to be RFC-correct; the report is a
// signal, not an authoritative URL list.
func extractURLs(cmd string) []string {
	var out []string
	for _, prefix := range []string{"http://", "https://"} {
		search := cmd
		for {
			idx := strings.Index(search, prefix)
			if idx < 0 {
				break
			}
			tail := search[idx:]
			end := strings.IndexAny(tail, " \t\n;\"'")
			if end < 0 {
				end = len(tail)
			}
			candidate := strings.TrimRight(tail[:end], ".,)]}")
			out = append(out, candidate)
			search = tail[end:]
		}
	}
	return out
}

func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// FormatMarkdown renders rep as the human-readable report the analyzer
// prints to stdout. The format is intentionally stable: it is what
// engineers paste into design memos and what the closeout doc cites.
func FormatMarkdown(rep Report, source string) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# Tool-Call Trace Analysis")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Source: `%s`\n\n", source)
	fmt.Fprintf(&b, "- total events: %d\n", rep.TotalEvents)
	fmt.Fprintf(&b, "- audit-tagged events: %d\n", rep.AuditEvents)
	fmt.Fprintf(&b, "- tool_call.started: %d (bash %d, curl %d, wget %d)\n\n",
		rep.ToolCallStarted, rep.BashCalls, rep.CurlCalls, rep.WgetCalls)

	writeCountTable(&b, "Bash command-head distribution", "head", rep.BashHeads)
	writeCountTable(&b, "Per-skill tallies", "skill", rep.PerSkillTallies)
	writeCountTable(&b, "URL frequencies (curl/wget)", "host", rep.URLsByHost)

	if len(rep.InnerBodies) > 0 {
		fmt.Fprintln(&b, "## Turing-complete inner bodies")
		fmt.Fprintln(&b)
		heads := make([]string, 0, len(rep.InnerBodies))
		for head := range rep.InnerBodies {
			heads = append(heads, head)
		}
		sort.Strings(heads)
		for _, head := range heads {
			bodies := rep.InnerBodies[head]
			fmt.Fprintf(&b, "### %s (%d)\n\n", head, len(bodies))
			for _, body := range bodies {
				fmt.Fprintf(&b, "```\n%s\n```\n\n", body)
			}
		}
	}
	return b.String()
}

func writeCountTable(b *strings.Builder, title, keyHeader string, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	fmt.Fprintf(b, "## %s\n\n", title)
	fmt.Fprintf(b, "| %s | count |\n| --- | --- |\n", keyHeader)
	type entry struct {
		k string
		n int
	}
	entries := make([]entry, 0, len(counts))
	for k, n := range counts {
		entries = append(entries, entry{k, n})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].n != entries[j].n {
			return entries[i].n > entries[j].n
		}
		return entries[i].k < entries[j].k
	})
	for _, e := range entries {
		fmt.Fprintf(b, "| %s | %d |\n", e.k, e.n)
	}
	fmt.Fprintln(b)
}
