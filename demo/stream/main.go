// Command stream-demo drives the internal/llm streaming API against a
// real LLM so you can watch reasoning and text fragments arrive in
// real time, and also observe prompt caching kick in across turns.
//
// It loads credentials via llm.NewClientFromEnv (reading the default .env file
// in the current working directory, or files passed with --env-file), builds
// one Conversation, and streams three short follow-up turns. Each turn prints:
//
//   - ReasoningDelta fragments in dim grey under a [reasoning] header
//     so it is visually obvious the model is thinking;
//   - TextDelta fragments verbatim under an [answer] header, the way
//     an end-user would see them;
//   - the per-request TokenUsage reported by the provider.
//
// The final section prints a per-turn usage table. Prompt caching on
// OpenAI's Responses API only activates once the shared prefix of a
// request is roughly 1024+ tokens, so turns 1 and 2 usually show
// cached=0 while turn 3 is where cached finally jumps above zero.
// Providers that do not report input_tokens_details.cached_tokens
// (OpenRouter, Gemini via the openai-go adapter) will leave cached=0
// throughout even when the upstream cached bytes.
//
// Run it from the repo root so the .env file is picked up:
//
//	go run ./cmd/demo/stream
//	go run ./cmd/demo/stream --env-file ./local.env
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/llm"
)

// ANSI escape sequences for colored section headers and dim
// reasoning text. They are printed unconditionally; on a terminal
// that does not understand them they show up as inert characters.
const (
	ansiReset   = "\x1b[0m"
	ansiDim     = "\x1b[2m"
	ansiCyan    = "\x1b[36m"
	ansiYellow  = "\x1b[33m"
	ansiMagenta = "\x1b[35m"
	ansiBold    = "\x1b[1m"
)

// prompt1, prompt2, and prompt3 drive the three streamed turns. The
// wording is open-ended enough that a reasoning model still spends
// real time thinking, but short enough that the whole demo finishes
// quickly. See systemInstruction for why the *system* text is what
// actually makes prompt caching visible: it provides a large stable
// byte-identical prefix that every request shares.
const prompt1 = `You are helping design a small, local-first note-taking app for engineers.
Sketch the storage layer in one page: compare (a) one SQLite DB, (b) many
Markdown files, (c) Markdown on disk + a derived SQLite index. Pick one and
justify it in 3-5 bullets. Keep it concrete.`

const prompt2 = `Good. Now add offline conflict resolution: two devices edit the same note
without sync. Describe the merge strategy and the on-disk layout that makes
it work. Keep it short.`

const prompt3 = `Last tweak: the app must also run on a phone with tiny storage, where the
derived SQLite index is too expensive. Which parts of the storage layer do
you drop or rebuild on demand, and how does search degrade gracefully?
One short paragraph is fine.`

// systemInstruction is intentionally long (well over 1024 tokens) so
// that every request's static prefix on its own already crosses
// OpenAI's prompt-caching threshold. Without this, a conversation
// whose user prompts are short may never accumulate a 1024-token
// shared prefix within the first few turns, and `cached` stays 0
// forever. The content here is just generic style and reasoning
// guidance - harmless to the answer but expensive enough in tokens
// to be cache-worthy.
const systemInstruction = `You are a seasoned, pragmatic senior software engineer acting as a
technical reviewer and designer. Your job in this conversation is
to help another engineer think through concrete systems-level
tradeoffs. Keep every answer grounded in real operational concerns
(latency, storage cost, failure modes, on-call burden, migration
effort, observability, backwards compatibility) rather than in
abstract architecture diagrams.

Writing style:
  - Prefer plain, declarative sentences over hedging or marketing
    language. "We do X because Y" is better than "It might be
    worth considering whether we could potentially do X".
  - Short paragraphs. Use bullet lists only when the content is
    genuinely a list of peers; do not bullet prose.
  - Inline code spans for API names, struct names, file paths,
    environment variables, SQL tables, CLI flags.
  - When you propose a design, always call out at least one
    explicit downside or failure mode of that design, not just
    its benefits.
  - When you compare options, use a short comparison table or an
    A vs. B vs. C block so the tradeoffs are obvious.
  - End any multi-step proposal with a short "first slice" list
    of the smallest thing that would prove the approach out in
    production.

Reasoning style:
  - Think about the data first: what entities exist, what their
    identity is, what their lifecycle looks like, and what the
    boundary of consistency is.
  - Then think about the I/O: who writes, who reads, how often,
    from which process, under which failure modes.
  - Then think about evolution: schema migration, rolling
    upgrade, feature flagging, rollback.
  - Only then talk about languages, frameworks, or libraries.
  - If a requirement is ambiguous, state the interpretation you
    are using before answering. Do not silently pick one.
  - If two reasonable interpretations lead to different designs,
    briefly sketch both and flag which you recommend and why.

Things you will not do:
  - You will not invent numbers. If you give a latency or a size,
    say where it comes from (a benchmark, a rule of thumb, or a
    guess labelled as a guess).
  - You will not recommend an exotic database, queue, or
    framework when a boring one (SQLite, Postgres, Redis, a
    cronjob, a flat file) solves the stated problem.
  - You will not add tracing, metrics, dashboards, kubernetes,
    service meshes, feature flagging systems, or any other
    operational machinery unless the stated requirement actually
    calls for it. Add them only when the user asks or when their
    absence is a correctness bug.
  - You will not bury the answer under preamble. Start with the
    recommendation, then justify it.

Context assumptions (stable across this conversation):
  - The target audience is a single experienced engineer, not a
    committee. Write as if you are pairing with them at a
    whiteboard.
  - The target platform is a single-tenant developer tool that
    runs on a laptop first and on a small server second. There
    is no "web scale" in this conversation.
  - The target failure model is: process can crash, disk can be
    full, the user can pull the laptop lid down mid-write,
    network can be absent for hours. Correctness under those
    conditions matters more than throughput.
  - The operational team is the engineer themselves. There is no
    SRE rotation. "Operability" therefore means "runs unattended
    on a laptop without daily intervention".

Quality bar:
  - Every proposal must either survive an fsync crash after the
    very last committed operation, or clearly document what is
    lost and why that loss is acceptable.
  - Every proposal that stores user data must say how the user
    gets their data out (export path, file format, tooling) even
    if that is just "it is a plain file on disk".
  - Every proposal that has a background job must say what
    happens when that job is stopped forever (user quits the
    app, forgets to relaunch, loses the binary).

When the user asks for a small adjustment to an earlier design,
keep the rest of the design stable. Do not rewrite the whole
architecture to accommodate a single new constraint; instead,
identify the smallest change to the existing plan that satisfies
the new constraint, and state what invariants you are preserving.

Self-check before sending:
  1. Did you state a recommendation up front?
  2. Did you list at least one explicit downside?
  3. Did you say how the data is evolved (migrated) when the
     schema needs to change?
  4. Did you say what a "first slice" ship of this would look
     like?
If any of those is missing, add it before replying.

Answer now, following the rules above.`

func main() {
	envFiles, err := parseEnvFiles(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Fatalf("parse command line: %v", err)
	}

	// Reasoning fragments only stream when the provider actually thinks,
	// and most providers only think when reasoning_effort is set. Default
	// the env var here so running the demo out of the box produces a
	// visible [reasoning] section; an operator who already exported
	// REASONING_EFFORT keeps full control.
	if os.Getenv("REASONING_EFFORT") == "" {
		os.Setenv("REASONING_EFFORT", string(llm.ReasoningEffortMedium))
	}

	client, err := llm.NewClientFromEnv(envFiles...)
	if err != nil {
		log.Fatalf("load llm client from env: %v", err)
	}

	fmt.Printf("%s========= 1. LLM client =========%s\n", ansiBold, ansiReset)
	fmt.Printf("  %s\n", client)
	if effort := client.ReasoningEffort(); effort == "" {
		fmt.Printf("  reasoning_effort: (unset; provider default)\n")
	} else {
		fmt.Printf("  reasoning_effort: %s\n", effort)
	}
	fmt.Printf("  stream categories: %s\n", describeCategories(client.StreamCategories()))
	fmt.Printf("  system instruction size: %d chars (≈%d tokens)\n",
		len(systemInstruction), len(systemInstruction)/4)
	fmt.Printf("  note: reasoning fragments only appear if the selected\n")
	fmt.Printf("        provider+model actually emits reasoning_text or\n")
	fmt.Printf("        reasoning_summary_text deltas. With non-reasoning\n")
	fmt.Printf("        models you will only see the [answer] section.\n")
	if client.Provider() != llm.ProviderOpenAI {
		fmt.Printf("  %swarning: provider is %q, not %q. cached_tokens is\n"+
			"           populated reliably only on OpenAI. OpenRouter and\n"+
			"           Gemini usage adapters often leave it at 0 even when\n"+
			"           the upstream actually hit its own cache.%s\n",
			ansiYellow, client.Provider(), llm.ProviderOpenAI, ansiReset)
	}

	conv, err := llm.NewConversation(client, systemInstruction, nil, nil)
	if err != nil {
		log.Fatalf("new conversation: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	fmt.Printf("\n%s========= 2. Streaming legend =========%s\n", ansiBold, ansiReset)
	fmt.Printf("%s[reasoning]%s = model thinking, %s[answer]%s = user-visible text. "+
		"Each '·' between sections is a section switch.\n",
		ansiYellow, ansiReset, ansiCyan, ansiReset)

	// Three turns. With a large stable system instruction the
	// cacheable prefix crosses the ~1024-token threshold on turn 1
	// already, so we expect turn 2 and turn 3 to report a nonzero
	// `cached` reflecting the shared prefix with the previous turn.
	usages := make([]llm.TokenUsage, 0, 3)
	for i, p := range []string{prompt1, prompt2, prompt3} {
		fmt.Printf("\n%s========= Turn %d prompt =========%s\n%s\n",
			ansiBold, i+1, ansiReset, p)
		conv.AppendUser(p)
		fmt.Printf("\n%s========= Turn %d stream =========%s\n", ansiBold, i+1, ansiReset)
		if err := streamAndRender(ctx, client, conv); err != nil {
			log.Fatalf("stream turn %d: %v", i+1, err)
		}
		u := conv.Usage()
		usages = append(usages, u)
		fmt.Printf("%sTurn %d usage:%s input=%d cached=%d output=%d reasoning=%d total=%d\n",
			ansiBold, i+1, ansiReset,
			u.Input, u.Cached, u.Output, u.Reasoning, u.Total)
	}

	fmt.Printf("\n%s========= Final conversation state =========%s\n", ansiBold, ansiReset)
	for i, t := range conv.Turns() {
		fmt.Printf("  [%d] %-10s %s\n", i, string(t.Role), describeTurn(t))
	}

	fmt.Printf("\n%s========= Usage per turn (Usage() is last-request, not cumulative) =========%s\n",
		ansiBold, ansiReset)
	fmt.Printf("  %-6s %8s %8s %8s %10s %8s\n",
		"turn", "input", "cached", "output", "reasoning", "total")
	for i, u := range usages {
		fmt.Printf("  %-6d %8d %8d %8d %10d %8d\n",
			i+1, u.Input, u.Cached, u.Output, u.Reasoning, u.Total)
	}
	fmt.Printf("\n%sExpected pattern:%s cached stays 0 while the shared prefix is\n"+
		"below the provider's caching threshold (OpenAI: ~1024 tokens), then jumps\n"+
		"on a later turn once the prefix crosses it. If cached stays 0 on turn 3 as\n"+
		"well, your provider+model likely does not report cached_tokens (OpenRouter\n"+
		"and Gemini usage adapters often don't), or the shared prefix is still too\n"+
		"short. Either way, the streaming path itself is unaffected.\n",
		ansiDim, ansiReset)

	if _, ok := os.LookupEnv("STREAM_DEMO_QUIET"); !ok {
		fmt.Printf("\n%sDone.%s\n", ansiMagenta, ansiReset)
	}
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

	flags := flag.NewFlagSet("stream-demo", flag.ContinueOnError)
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

// streamAndRender drives one Client.Stream call to completion,
// printing reasoning and answer fragments under colored section
// headers and then a short per-turn summary block. It is called once
// for the initial user turn and a second time after a follow-up turn
// is appended, so the operator can watch cached-token growth across
// the two requests.
func streamAndRender(ctx context.Context, client *llm.Client, conv *llm.Conversation) error {
	ch, err := conv.Stream(ctx, "")
	if err != nil {
		return err
	}

	start := time.Now()
	var (
		section            string // "reasoning" | "answer" | ""
		firstReasoningAt   time.Duration
		firstTextAt        time.Duration
		reasoningChars     int
		textChars          int
		streamEvents       int
		terminatingErr     error
		switchedAtLeastOne bool
	)

	for ev := range ch {
		streamEvents++
		switch {
		case ev.Err != nil:
			terminatingErr = ev.Err
		case ev.ReasoningDelta != "":
			if section != "reasoning" {
				if section != "" {
					fmt.Printf("%s\n%s ·\n", ansiReset, ansiDim)
					switchedAtLeastOne = true
				}
				fmt.Printf("%s%s[reasoning]%s%s ", ansiYellow, ansiBold, ansiReset, ansiDim)
				section = "reasoning"
			}
			if firstReasoningAt == 0 {
				firstReasoningAt = time.Since(start)
			}
			reasoningChars += len(ev.ReasoningDelta)
			fmt.Print(ev.ReasoningDelta)
		case ev.TextDelta != "":
			if section != "answer" {
				if section != "" {
					fmt.Printf("%s\n%s ·\n%s", ansiReset, ansiDim, ansiReset)
					switchedAtLeastOne = true
				}
				fmt.Printf("%s%s[answer]%s ", ansiCyan, ansiBold, ansiReset)
				section = "answer"
			}
			if firstTextAt == 0 {
				firstTextAt = time.Since(start)
			}
			textChars += len(ev.TextDelta)
			fmt.Print(ev.TextDelta)
		}
	}
	// Restore default formatting after the last fragment so the
	// summary block prints in normal weight.
	fmt.Printf("%s\n", ansiReset)

	if terminatingErr != nil {
		return terminatingErr
	}

	total := time.Since(start)
	fmt.Printf("\n%sStream summary:%s\n", ansiBold, ansiReset)
	fmt.Printf("  events received: %d\n", streamEvents)
	fmt.Printf("  reasoning chars streamed: %d\n", reasoningChars)
	fmt.Printf("  text chars streamed:      %d\n", textChars)
	if firstReasoningAt > 0 {
		fmt.Printf("  first reasoning delta @  %s\n", firstReasoningAt.Round(time.Millisecond))
	} else {
		fmt.Printf("  first reasoning delta:   (none — model did not stream reasoning)\n")
	}
	if firstTextAt > 0 {
		fmt.Printf("  first answer delta @     %s\n", firstTextAt.Round(time.Millisecond))
	} else {
		fmt.Printf("  first answer delta:      (none — model only streamed reasoning)\n")
	}
	fmt.Printf("  total stream time:       %s\n", total.Round(time.Millisecond))
	if !switchedAtLeastOne {
		fmt.Printf("  note: no section switch happened. With a reasoning model\n")
		fmt.Printf("        and StreamCategories=Text|Reasoning you should see\n")
		fmt.Printf("        reasoning fragments first, then answer fragments.\n")
	}
	return nil
}

// describeCategories renders the active StreamCategory bitmask as a
// short human-readable list so the operator can confirm which
// fragment kinds the demo is asking for.
func describeCategories(s llm.StreamCategory) string {
	parts := make([]string, 0, 2)
	if s.Has(llm.StreamText) {
		parts = append(parts, "text")
	}
	if s.Has(llm.StreamReasoning) {
		parts = append(parts, "reasoning")
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, "+")
}

// describeTurn renders a single turn compactly for the post-stream
// summary so it is easy to verify the conversation captured the
// expected items (user prompt, optional reasoning, assistant text).
func describeTurn(t llm.Turn) string {
	switch t.Role {
	case llm.RoleUser, llm.RoleAssistant:
		return truncate(oneLine(t.Text), 120)
	case llm.RoleReasoning:
		raw := ""
		if len(t.Raw) > 0 {
			raw = fmt.Sprintf(" raw=%dB", len(t.Raw))
		}
		return fmt.Sprintf("%s%s", truncate(oneLine(t.Text), 100), raw)
	case llm.RoleToolCall:
		if t.Tool != nil {
			return fmt.Sprintf("%s(%s)", t.Tool.Name, truncate(oneLine(t.Tool.Arguments), 80))
		}
	case llm.RoleToolOutput:
		if t.Tool != nil {
			return truncate(oneLine(t.Tool.Output), 120)
		}
	}
	return ""
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
