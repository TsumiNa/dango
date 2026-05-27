package main

import (
	"encoding/json"
	"strings"
	"testing"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

// fixture builds a JSON-lines stream-event log with controllable contents
// so each test can assert on one analyzer behavior in isolation.
type fixture struct {
	t    *testing.T
	buf  strings.Builder
	seq  uint64
	tick uint64
}

func newFixture(t *testing.T) *fixture { return &fixture{t: t} }

func (f *fixture) addAuditToolCall(skill, tool, args string) {
	f.t.Helper()
	f.seq++
	f.tick++
	delta, err := json.Marshal(map[string]any{
		"name":      tool,
		"call_id":   "call_" + tool,
		"arguments": args,
	})
	if err != nil {
		f.t.Fatalf("marshal delta: %v", err)
	}
	ev := streampkg.Event{
		EventType:      streampkg.EventLLMToolCallStarted,
		From:           streampkg.Source{Layer: "skill", ID: skill},
		SequenceNumber: f.seq,
		Status:         streampkg.StatusRunning,
		Delta:          delta,
		LogicalTime:    f.tick,
		Metadata: map[string]any{
			"category":   "audit",
			"skill_name": skill,
		},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		f.t.Fatalf("marshal event: %v", err)
	}
	f.buf.Write(raw)
	f.buf.WriteByte('\n')
}

func (f *fixture) addNonAudit() {
	f.t.Helper()
	f.seq++
	ev := streampkg.Event{
		EventType:      streampkg.EventLLMReasoningDelta,
		From:           streampkg.Source{Layer: "skill", ID: "irrelevant"},
		SequenceNumber: f.seq,
		Status:         streampkg.StatusRunning,
		Delta:          json.RawMessage(`"thinking..."`),
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		f.t.Fatalf("marshal event: %v", err)
	}
	f.buf.Write(raw)
	f.buf.WriteByte('\n')
}

func (f *fixture) reader() *strings.Reader { return strings.NewReader(f.buf.String()) }

// bashArgs is a convenience for building the `arguments` JSON the bash
// tool emits.
func bashArgs(cmd string) string {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return string(b)
}

func TestAnalyzeIgnoresNonAuditAndCountsAuditEvents(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.addAuditToolCall("writer", "bash", bashArgs("ls /tmp"))
	f.addNonAudit()

	rep, err := Analyze(f.reader())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.TotalEvents != 2 || rep.AuditEvents != 1 || rep.ToolCallStarted != 1 {
		t.Fatalf("counts mismatch: %+v", rep)
	}
	if rep.PerSkillTallies["writer"] != 1 {
		t.Fatalf("per-skill tally missed: %+v", rep.PerSkillTallies)
	}
}

func TestAnalyzerSummarizesBashHeads(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.addAuditToolCall("a", "bash", bashArgs("ls /tmp"))
	f.addAuditToolCall("a", "bash", bashArgs("ls -la"))
	f.addAuditToolCall("a", "bash", bashArgs("git status"))

	rep, err := Analyze(f.reader())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.BashHeads["ls"] != 2 {
		t.Fatalf("ls count = %d, want 2; heads=%+v", rep.BashHeads["ls"], rep.BashHeads)
	}
	if rep.BashHeads["git"] != 1 {
		t.Fatalf("git count = %d, want 1; heads=%+v", rep.BashHeads["git"], rep.BashHeads)
	}
}

func TestCommandHeadSkipsLeadingAssignments(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"FOO=bar baz qux":         "baz",
		"A=1 B=2 python3 -c 'x'":  "python3",
		"normal-cmd arg":          "normal-cmd",
		"":                        "",
		"FOO= cmd":                "cmd", // empty-value assignment still skipped
		"1BAD=skip cmd":           "1BAD=skip", // name cannot start with a digit
	}
	for in, want := range cases {
		if got := commandHead(in); got != want {
			t.Errorf("commandHead(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnalyzerCapturesInnerCommandBodies(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.addAuditToolCall("a", "bash", bashArgs(`python3 -c "print('hi')"`))
	f.addAuditToolCall("a", "bash", bashArgs(`bash -c 'echo $HOME'`))
	f.addAuditToolCall("a", "bash", bashArgs("awk '{print $2}' /tmp/f"))
	f.addAuditToolCall("a", "bash", bashArgs("xargs -n1 grep needle"))
	f.addAuditToolCall("a", "bash", bashArgs("python3 <<'PY'\nprint(1)\nprint(2)\nPY\n"))

	rep, err := Analyze(f.reader())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	pyBodies := rep.InnerBodies["python3"]
	if len(pyBodies) != 2 {
		t.Fatalf("expected 2 python3 bodies, got %d: %+v", len(pyBodies), pyBodies)
	}
	if !contains(pyBodies, "print('hi')") {
		t.Fatalf("missing python -c body: %+v", pyBodies)
	}
	if !containsSubstring(pyBodies, "print(1)") {
		t.Fatalf("missing python heredoc body: %+v", pyBodies)
	}

	bashBodies := rep.InnerBodies["bash"]
	if len(bashBodies) != 1 || bashBodies[0] != "echo $HOME" {
		t.Fatalf("bash -c body: %+v", bashBodies)
	}

	awkBodies := rep.InnerBodies["awk"]
	if len(awkBodies) != 1 || !strings.Contains(awkBodies[0], "print") {
		t.Fatalf("awk script body: %+v", awkBodies)
	}

	xargsBodies := rep.InnerBodies["xargs"]
	if len(xargsBodies) != 1 || !strings.HasPrefix(xargsBodies[0], "grep") {
		t.Fatalf("xargs wrapped command: %+v", xargsBodies)
	}
}

func TestAnalyzerCountsURLsByHost(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.addAuditToolCall("a", "bash", bashArgs("curl https://api.example.com/v1/items"))
	f.addAuditToolCall("a", "bash", bashArgs("curl -L https://api.example.com/v1/users"))
	f.addAuditToolCall("a", "bash", bashArgs("wget http://files.archive.org/file.zip"))
	f.addAuditToolCall("a", "bash", bashArgs("ls /tmp")) // not a URL emitter

	rep, err := Analyze(f.reader())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.CurlCalls != 2 {
		t.Fatalf("CurlCalls = %d, want 2", rep.CurlCalls)
	}
	if rep.WgetCalls != 1 {
		t.Fatalf("WgetCalls = %d, want 1", rep.WgetCalls)
	}
	if rep.URLsByHost["api.example.com"] != 2 {
		t.Fatalf("api.example.com count = %d, want 2; %+v", rep.URLsByHost["api.example.com"], rep.URLsByHost)
	}
	if rep.URLsByHost["files.archive.org"] != 1 {
		t.Fatalf("files.archive.org count = %d, want 1; %+v", rep.URLsByHost["files.archive.org"], rep.URLsByHost)
	}
}

func TestAnalyzerPerSkillFallsBackToFromID(t *testing.T) {
	t.Parallel()
	// Build an audit event without metadata.skill_name so the analyzer has
	// to fall back to From.ID.
	ev := streampkg.Event{
		EventType:      streampkg.EventLLMToolCallStarted,
		From:           streampkg.Source{Layer: "skill", ID: "fallback_skill"},
		SequenceNumber: 1,
		Status:         streampkg.StatusRunning,
		Delta:          json.RawMessage(`{"name":"bash","call_id":"c","arguments":"{\"command\":\"ls\"}"}`),
		Metadata:       map[string]any{"category": "audit"},
	}
	raw, _ := json.Marshal(ev)
	rep, err := Analyze(strings.NewReader(string(raw) + "\n"))
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.PerSkillTallies["fallback_skill"] != 1 {
		t.Fatalf("fallback tally: %+v", rep.PerSkillTallies)
	}
}

func TestAnalyzerTolerantOfBadLines(t *testing.T) {
	t.Parallel()
	input := strings.NewReader("{not json}\n" + makeAuditLine(t, "writer", "bash", bashArgs("ls /tmp")) + "\n")
	rep, err := Analyze(input)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if rep.TotalEvents != 2 || rep.AuditEvents != 1 {
		t.Fatalf("expected to tolerate one bad line; rep=%+v", rep)
	}
}

// TestAnalyzerHandlesOverlongLines guards the bufio.Reader switch: a single
// stream event that exceeds the old bufio.Scanner 1 MiB cap (memo
// snapshots and exchange document deltas can be megabytes) must not abort
// the whole analysis, and the tool-call lines around it must still tally.
func TestAnalyzerHandlesOverlongLines(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("x", 2<<20) // 2 MiB single line — exceeds the old cap
	input := strings.NewReader(
		makeAuditLine(t, "writer", "bash", bashArgs("ls /tmp")) + "\n" +
			huge + "\n" +
			makeAuditLine(t, "writer", "bash", bashArgs("git status")) + "\n",
	)

	rep, err := Analyze(input)
	if err != nil {
		t.Fatalf("Analyze must not abort on overlong line: %v", err)
	}
	if rep.TotalEvents != 3 {
		t.Fatalf("TotalEvents = %d, want 3 (one huge line still counted)", rep.TotalEvents)
	}
	if rep.BashHeads["ls"] != 1 || rep.BashHeads["git"] != 1 {
		t.Fatalf("tool-call tally lost across the overlong line: %+v", rep.BashHeads)
	}
}

func TestFormatMarkdownIncludesAllSections(t *testing.T) {
	t.Parallel()
	rep := Report{
		TotalEvents:     5,
		AuditEvents:     3,
		ToolCallStarted: 3,
		BashCalls:       2,
		CurlCalls:       1,
		BashHeads:       map[string]int{"ls": 1, "curl": 1},
		PerSkillTallies: map[string]int{"writer": 2, "reader": 1},
		URLsByHost:      map[string]int{"api.example.com": 1},
		InnerBodies: map[string][]string{
			"python3": {"print('x')"},
		},
	}
	md := FormatMarkdown(rep, "fixture.jsonl")
	for _, want := range []string{
		"# Tool-Call Trace Analysis",
		"Bash command-head distribution",
		"Per-skill tallies",
		"URL frequencies",
		"Turing-complete inner bodies",
		"### python3 (1)",
		"print('x')",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q\n---\n%s", want, md)
		}
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, want string) bool {
	for _, v := range values {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}

func makeAuditLine(t *testing.T, skill, tool, args string) string {
	t.Helper()
	delta, _ := json.Marshal(map[string]any{"name": tool, "arguments": args, "call_id": "c"})
	ev := streampkg.Event{
		EventType:      streampkg.EventLLMToolCallStarted,
		From:           streampkg.Source{Layer: "skill", ID: skill},
		SequenceNumber: 1,
		Status:         streampkg.StatusRunning,
		Delta:          delta,
		Metadata:       map[string]any{"category": "audit", "skill_name": skill},
	}
	raw, _ := json.Marshal(ev)
	return string(raw)
}
