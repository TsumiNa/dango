// Command skill-demo drives the Skill API in internal/llm against a real LLM.
//
// It loads credentials via llm.NewClientFromEnv (reading the default .env file
// in the current working directory, or files passed with --env-file), creates a
// temporary skill directory, registers the built-in filesystem/shell tools plus
// one custom tool, and lets the model orchestrate them. The demo also exercises
// the conversation runtime features used by Skill.Bind:
//
//   - tier-aware auto-shrink via llm.ConversationConfig.AutoShrink;
//   - LLM-backed summarization via llm.ConversationConfig.Summarizer;
//   - cross-run session persistence via Skill.Bind and llm.JSONStore.
//
// When the REASONING_EFFORT environment variable is set, llm.Client
// forwards it on every request. Because OpenAI SDK compatibility varies
// across providers (Gemini maps it to thinking_level/thinking_budget,
// Anthropic's OpenAI-compat layer ignores it, local runners differ), the
// demo prints the per-run reasoning token count so the operator can
// verify whether the configured effort actually took effect against the
// selected provider.
//
// Run it from the repo root so the .env file is picked up:
//
//	go run ./cmd/demo/skill
//	go run ./cmd/demo/skill --env-file ./local.env
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/llm"
)

// ANSI styling helpers. The demo is intended for terminal use, so colors are
// on by default. Set NO_COLOR=1 to disable them.
var colorEnabled = os.Getenv("NO_COLOR") == ""

const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiDim     = "\x1b[2m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiBlue    = "\x1b[34m"
	ansiMagenta = "\x1b[35m"
	ansiCyan    = "\x1b[36m"
)

func style(codes, s string) string {
	if !colorEnabled {
		return s
	}
	return codes + s + ansiReset
}

func bold(s string) string    { return style(ansiBold, s) }
func dim(s string) string     { return style(ansiDim, s) }
func red(s string) string     { return style(ansiRed, s) }
func green(s string) string   { return style(ansiGreen, s) }
func yellow(s string) string  { return style(ansiYellow, s) }
func blue(s string) string    { return style(ansiBlue, s) }
func magenta(s string) string { return style(ansiMagenta, s) }
func cyan(s string) string    { return style(ansiCyan, s) }

func banner(step int, title, intent string) {
	label := fmt.Sprintf(" STEP %d | %s ", step, title)
	line := strings.Repeat("=", len([]rune(label)))
	fmt.Println()
	fmt.Println(blue(bold(line)))
	fmt.Println(blue(bold(label)))
	fmt.Println(blue(bold(line)))
	if intent != "" {
		fmt.Println(dim("  shows: ") + intent)
	}
}

func field(label, value string) {
	fmt.Printf("  %s %s\n", cyan(label+":"), value)
}

func note(msg string)     { fmt.Println(dim("  - ") + msg) }
func okLine(msg string)   { fmt.Println(green("  ok ") + msg) }
func warnLine(msg string) { fmt.Println(yellow("  !  ") + msg) }

func printBlock(label, body string, color func(string) string) {
	fmt.Println(color("  " + label + ":"))
	fmt.Println(indentBlock(body, "    "))
}

func indentBlock(s, prefix string) string {
	if s == "" {
		return prefix + dim("(empty)")
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func main() {
	envFiles, err := parseEnvFiles(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Fatalf("parse command line: %v", err)
	}

	client, err := llm.NewClientFromEnv(envFiles...)
	if err != nil {
		log.Fatalf("load llm client from env: %v", err)
	}
	fmt.Println(bold("Dango skill demo") + dim(" - New + Bind + tool loop + persisted session"))
	note("The demo creates a temporary skill workspace, lets the LLM call tools, saves the event log, then restores it for a second run.")

	banner(1, "LLM client", "which model and reasoning settings will drive Skill.Run")
	field("client", client.String())
	effort := client.ReasoningEffort()
	if effort == "" {
		field("reasoning_effort", dim("unset; provider default"))
	} else {
		field("reasoning_effort", string(effort))
	}
	warnLine("OpenAI SDK compatibility varies across providers; the usage line below shows whether reasoning tokens were actually reported.")

	dir, err := os.MkdirTemp("", "skill-demo-")
	if err != nil {
		log.Fatalf("mkdir temp: %v", err)
	}
	defer os.RemoveAll(dir)

	skillMD := "---\n" +
		"name: demo-skill\n" +
		"description: Demo skill that exercises the built-in tools.\n" +
		"license: MIT\n" +
		"---\n" +
		"You are a careful assistant running inside a sandboxed workspace.\n" +
		"You have filesystem tools (list_dir, read_file, write_file), a shell\n" +
		"tool (bash), pwd, and a custom greet tool.\n" +
		"\n" +
		"When the user gives you a task, plan briefly, then call tools to\n" +
		"accomplish it. After the task is complete, reply with a short summary\n" +
		"of the steps you took.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		log.Fatalf("write SKILL.md: %v", err)
	}
	banner(2, "Skill workspace", "the temporary directory passed to llm.New")
	field("workspace", dir)
	field("skill file", filepath.Join(dir, "SKILL.md"))
	printBlock("SKILL.md prompt body", frontmatterBody(skillMD), dim)

	bashAllow := []string(nil)
	bashBlock := []string{"curl", "wget"}
	banner(3, "Tools", "built-in workspace tools plus one custom greet tool")
	field("bash allow additions", formatSlice(bashAllow))
	field("bash block removals", formatSlice(bashBlock))

	baseSkill, err := llm.NewSkill(dir, llm.SkillConfig{BashAllow: bashAllow, BashBlock: bashBlock})
	if err != nil {
		log.Fatalf("load skill: %v", err)
	}
	builtinTools, err := baseSkill.BuiltinTools()
	if err != nil {
		log.Fatalf("load built-in tools: %v", err)
	}
	tracedBuiltins := traceTools(builtinTools)
	greet := traceTool(llm.NewFuncTool(
		"greet",
		"Produce a friendly greeting for the provided name.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Person to greet.",
				},
			},
			"required":             []string{"name"},
			"additionalProperties": false,
		},
		func(_ context.Context, arguments string) (string, error) {
			var a struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal([]byte(arguments), &a); err != nil {
				return "", err
			}
			return fmt.Sprintf("Hello, %s! Welcome to dango.", a.Name), nil
		},
	))

	allTools := append(tracedBuiltins, greet)

	fmt.Println(cyan("  registered tools:"))
	for _, t := range allTools {
		fmt.Printf("    %s %s\n", magenta(fmt.Sprintf("%-12s", t.Name())), dim(t.Description()))
	}

	// Phase A: auto-shrink policy. ContextWindow is intentionally small
	// so the demo can exercise the shrink path on modest traffic; in
	// production callers should set this to the real model window.
	shrinkCfg := llm.AutoShrinkConfig{
		ContextWindow:     8000,
		Threshold:         0.85,
		KeepToolExchanges: 2,
		KeepTurns:         8,
	}

	// Phase B: LLM-backed summarizer. Summarize runs inside Client.Send
	// when window pressure crosses the threshold, so it must not call
	// Send on the same conversation; using Client.Respond with a fresh
	// prompt avoids any recursion.
	summarizer := llm.SummarizerFunc(func(ctx context.Context, turns []llm.Turn) (string, error) {
		var b strings.Builder
		for _, t := range turns {
			switch t.Role {
			case llm.RoleUser:
				fmt.Fprintf(&b, "USER: %s\n", t.Text)
			case llm.RoleAssistant:
				fmt.Fprintf(&b, "ASSISTANT: %s\n", t.Text)
			case llm.RoleToolCall:
				if t.Tool != nil {
					fmt.Fprintf(&b, "TOOL_CALL %s(%s)\n", t.Tool.Name, oneLine(t.Tool.Arguments))
				}
			case llm.RoleToolOutput:
				if t.Tool != nil {
					fmt.Fprintf(&b, "TOOL_OUTPUT: %s\n", truncate(oneLine(t.Tool.Output), 200))
				}
			}
		}
		fmt.Printf("  -> summarizer invoked on %d turns\n", len(turns))
		prompt := "Summarize the following conversation transcript in at most " +
			"3 short sentences, preserving any file paths, tool names, and " +
			"concrete results that later turns may need to reference.\n\n" +
			b.String()
		return client.Respond(ctx, prompt)
	})

	// Phase C: persistent session backed by a JSON file on disk.
	sessionsDir := filepath.Join(dir, "sessions")
	store, err := llm.NewJSONStore(sessionsDir)
	if err != nil {
		log.Fatalf("new session store: %v", err)
	}
	baseSkill, err = baseSkill.AddTools(allTools...)
	if err != nil {
		log.Fatalf("configure skill tools: %v", err)
	}
	convCfg := llm.ConversationConfig{
		MaxSteps:   12,
		AutoShrink: &shrinkCfg,
		Summarizer: summarizer,
	}
	banner(4, "Runtime binding", "Skill.Bind creates the runnable conversation and opens a JSONStore session")
	field("session store", sessionsDir)
	field("max steps", fmt.Sprintf("%d", convCfg.MaxSteps))
	field("auto shrink", fmt.Sprintf("window=%d threshold=%.0f%% keep_turns=%d keep_tool_exchanges=%d",
		shrinkCfg.ContextWindow, shrinkCfg.Threshold*100, shrinkCfg.KeepTurns, shrinkCfg.KeepToolExchanges))
	okLine("summarizer configured with a separate Client.Respond call")
	sk, err := baseSkill.Bind(client, convCfg, llm.WithNewSession(store))
	if err != nil {
		log.Fatalf("bind skill: %v", err)
	}
	sessionID := sk.Conversation().SessionID()
	field("session id", sessionID)

	firstInput := "Write a file named haiku.txt in the workspace containing a " +
		"three-line haiku about dango. Then read it back to verify, and " +
		"finally call the greet tool with the name \"Ada\". Summarize what " +
		"you did."

	banner(5, "Run 1 | fresh session", "the model should create haiku.txt, read it back, and call greet")
	printBlock("user task", firstInput, cyan)
	out1, err := sk.Run(context.Background(), firstInput, "")
	if err != nil {
		log.Fatalf("skill run 1: %v", err)
	}
	printBlock("assistant output", out1, green)
	reportSession(store, sessionID)

	// Second run: the saved session is loaded and the follow-up user
	// input is appended on top of the prior conversation, so the model
	// already knows about haiku.txt and Ada without being re-told.
	secondInput := "Now append a second haiku to haiku.txt (about sakura), " +
		"read the whole file back, and greet \"Grace\". Keep your summary short."

	banner(6, "Run 2 | restored session", "the saved event log is replayed before the follow-up task")
	printBlock("user task", secondInput, cyan)
	restored, err := baseSkill.Bind(client, convCfg, llm.WithExistingSession(sessionID, store))
	if err != nil {
		log.Fatalf("restore skill session: %v", err)
	}
	out2, err := restored.Run(context.Background(), secondInput, "")
	if err != nil {
		log.Fatalf("skill run 2: %v", err)
	}
	printBlock("assistant output", out2, green)
	reportSession(store, sessionID)

	banner(7, "Workspace after runs", "files created or changed by tool calls")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		suffix := ""
		if e.IsDir() {
			suffix = "/"
		}
		fmt.Printf("  %s %s%s\n", cyan("file"), e.Name(), suffix)
		if e.Name() == "haiku.txt" {
			if data, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
				printBlock("haiku.txt", string(data), magenta)
			}
		}
	}

	banner(8, "Session file on disk", "the append-only JSONL event log backing this conversation")
	dumpSessionFile(sessionsDir, sessionID)
}

func parseEnvFiles(args []string, output io.Writer) ([]string, error) {
	if output == nil {
		output = io.Discard
	}
	var envFiles []string
	addEnvFile := func(value string) error {
		if value == "" {
			return fmt.Errorf("env file path must not be empty")
		}
		envFiles = append(envFiles, value)
		return nil
	}

	flags := flag.NewFlagSet("skill-demo", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Func("env-file", "Path to an env file for llm.NewClientFromEnv; may be repeated.", addEnvFile)
	flags.Func("e", "Alias for --env-file.", addEnvFile)
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() > 0 {
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	return envFiles, nil
}

// reportSession reloads the persisted session log and prints a compact
// summary of its event count and the last recorded token usage. This
// demonstrates that the event-log JSONStore actually wrote something to
// disk after each run.
func reportSession(store *llm.JSONStore, id string) {
	events, err := store.Load(context.Background(), id)
	if err != nil {
		warnLine("session load error: " + err.Error())
		return
	}
	var lastUsage llm.TokenUsage
	var lastTS time.Time
	turns := 0
	for _, ev := range events {
		switch ev.Kind {
		case llm.EventAppendUser, llm.EventAppendAssistant,
			llm.EventAppendReasoning, llm.EventAppendToolCall, llm.EventAppendToolOutput:
			turns++
		case llm.EventRecordUsage:
			if ev.Usage != nil {
				lastUsage = *ev.Usage
			}
		}
		if ev.Timestamp.After(lastTS) {
			lastTS = ev.Timestamp
		}
	}
	fmt.Printf("  %s id=%s events=%d turns=%d updated=%s\n",
		cyan("session"), id, len(events), turns, lastTS.Format("15:04:05"))
	fmt.Printf("  %s input=%d cached=%d output=%d reasoning=%d total=%d\n",
		magenta("last usage"), lastUsage.Input, lastUsage.Cached, lastUsage.Output, lastUsage.Reasoning, lastUsage.Total)
}

// dumpSessionFile prints the JSONL file backing the session so the
// operator can see exactly what JSONStore wrote to disk: the absolute
// path, the file size and line count, the first event in pretty-printed
// form (the EventInit anchor with instructions and tool schema), and
// the last few events in compact one-line-per-event form. The middle of
// long logs is elided so the output stays readable.
func dumpSessionFile(sessionsDir, id string) {
	path := filepath.Join(sessionsDir, id+".jsonl")
	info, err := os.Stat(path)
	if err != nil {
		warnLine(fmt.Sprintf("stat %s: %v", path, err))
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		warnLine(fmt.Sprintf("read %s: %v", path, err))
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	field("path", path)
	field("size", fmt.Sprintf("%d bytes, %d events (one JSON object per line)", info.Size(), len(lines)))

	// First event: EventInit. Pretty-print it because it carries the
	// instructions and tool schema and shows what anchors the cache.
	if len(lines) > 0 {
		fmt.Println()
		fmt.Println(blue(bold("  event 1 | init anchor")))
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, []byte(lines[0]), "  ", "  "); err == nil {
			fmt.Println(indentBlock(pretty.String(), "    "))
		} else {
			fmt.Println(indentBlock(lines[0], "    "))
		}
	}

	// Tail: print up to the last 5 events compactly so the operator
	// can see the actual append-only turn shape (user / tool_call /
	// tool_output / assistant / record_usage).
	const tail = 5
	start := len(lines) - tail
	if start <= 1 {
		start = 1
	}
	if start < len(lines) {
		if start > 1 {
			fmt.Printf("\n%s\n", dim(fmt.Sprintf("  events 2..%d elided", start)))
		}
		fmt.Printf("\n%s\n", blue(bold(fmt.Sprintf("  last %d events | compact", len(lines)-start))))
		for _, line := range lines[start:] {
			fmt.Printf("    %s\n", truncate(line, 240))
		}
	}
}

type tracedTool struct {
	inner llm.Tool
}

func (t *tracedTool) Name() string               { return t.inner.Name() }
func (t *tracedTool) Description() string        { return t.inner.Description() }
func (t *tracedTool) Parameters() map[string]any { return t.inner.Parameters() }
func (t *tracedTool) Execute(ctx context.Context, arguments string) (string, error) {
	fmt.Printf("  %s %s %s\n", cyan("tool call"), bold(t.inner.Name()), dim("args="+oneLine(arguments)))
	out, err := t.inner.Execute(ctx, arguments)
	if err != nil {
		fmt.Printf("  %s %v\n", red("tool error"), err)
	}
	fmt.Printf("  %s %s\n", green("tool output"), truncate(oneLine(out), 160))
	return out, err
}

func traceTool(t llm.Tool) llm.Tool { return &tracedTool{inner: t} }

func traceTools(tools []llm.Tool) []llm.Tool {
	out := make([]llm.Tool, len(tools))
	for i, t := range tools {
		out[i] = traceTool(t)
	}
	return out
}

func oneLine(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n', '\r', '\t':
			b = append(b, ' ')
		default:
			b = append(b, s[i])
		}
	}
	return string(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func formatSlice(values []string) string {
	if len(values) == 0 {
		return dim("none")
	}
	return strings.Join(values, ", ")
}

func frontmatterBody(doc string) string {
	parts := strings.SplitN(doc, "---\n", 3)
	if len(parts) != 3 {
		return doc
	}
	return parts[2]
}
