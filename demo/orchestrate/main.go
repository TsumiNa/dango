package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	orchestrate "github.com/tsumina/dango/internal/engine"
	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	"github.com/tsumina/dango/internal/llm"
)

// ANSI styling helpers. The demo is intended for terminal use, so we always
// emit escape codes. Set NO_COLOR=1 to disable.
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

// runnerColors assigns each demo runner a distinct accent color so interleaved
// subscription events are easy to separate visually.
var runnerColors = map[string]string{
	"A": ansiCyan,
	"B": ansiMagenta,
	"C": ansiYellow,
}

func runnerTag(label string) string {
	code, ok := runnerColors[label]
	if !ok {
		code = ansiBlue
	}
	return style(ansiBold+code, fmt.Sprintf("[%s]", label))
}

// banner prints a highly visible section header so each demo stage is obvious.
func banner(step int, title, intent string) {
	head := fmt.Sprintf(" STEP %d · %s ", step, title)
	line := strings.Repeat("─", len([]rune(head)))
	fmt.Println()
	fmt.Println(style(ansiBold+ansiBlue, "┌"+line+"┐"))
	fmt.Println(style(ansiBold+ansiBlue, "│") + bold(head) + style(ansiBold+ansiBlue, "│"))
	fmt.Println(style(ansiBold+ansiBlue, "└"+line+"┘"))
	if intent != "" {
		fmt.Println(dim("  intent: ") + intent)
	}
}

func note(msg string)     { fmt.Println(dim("  · ") + msg) }
func okLine(msg string)   { fmt.Println(green("  ✓ ") + msg) }
func warnLine(msg string) { fmt.Println(yellow("  ! ") + msg) }

// colorPhase / colorStatus / colorEvent tint state strings so the eye can
// track lifecycle transitions at a glance.
func colorPhase(p runnerpkg.RunnerPhase) string {
	s := string(p)
	switch p {
	case runnerpkg.PhaseAwaitingReview, runnerpkg.PhaseAwaitingReplan:
		return yellow(s)
	case runnerpkg.PhaseExecuting:
		return cyan(s)
	case runnerpkg.PhaseReport:
		return blue(s)
	case runnerpkg.PhaseSettled:
		return green(s)
	case runnerpkg.PhasePolishing:
		return magenta(s)
	default:
		return s
	}
}

func colorStatus(s runnerpkg.RunnerStatus) string {
	str := string(s)
	switch s {
	case runnerpkg.RunnerStatusRunning:
		return cyan(str)
	case runnerpkg.RunnerStatusIdle:
		return green(str)
	case runnerpkg.RunnerStatusFailed, runnerpkg.RunnerStatusCanceled:
		return red(str)
	default:
		return dim(str)
	}
}

func colorEvent(t string) string {
	lower := strings.ToLower(t)
	switch {
	case strings.Contains(lower, "error") || strings.Contains(lower, "fail"):
		return red(t)
	case strings.Contains(lower, "complete") || strings.Contains(lower, "success"):
		return green(t)
	case strings.Contains(lower, "start"):
		return cyan(t)
	default:
		return yellow(t)
	}
}

func main() {
	// Keep the orchestrator's internal logs out of the demo stream so the
	// curated output stays readable. Raise to Info for debugging.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx := context.Background()

	o, cleanup := configureDemoOrchestrator(ctx, logger)
	defer cleanup()

	fmt.Println(bold("Dango orchestrator demo") + dim(" — runner-owned lifecycle with a single execution slot"))

	banner(1, "Configure orchestrator",
		"one execution slot + placeholder skills collect/review/summarize")
	note("max running runners: " + bold("1"))
	note("flow: " + cyan("plan → polish → review → execute → report → settled"))

	banner(2, "Request A · managed lifecycle",
		"StartRequest returns immediately; the runner owns review, execution, and report")
	firstPlan := mustStartRequest(ctx, o, "post this week's engineering update to Slack", orchestrate.RequestPriorityDefault)
	stopA := mustWatchRunner(o, "A", firstPlan.RunnerID)
	defer stopA()
	mustWaitForPhase(o, firstPlan.RunnerID, runnerpkg.PhaseSettled, 3*time.Second, "A settled")
	runnerA := mustRunner(o, firstPlan.RunnerID)
	printPlan("A plan", firstPlan)
	printValues("A report summaries", runnerA.ReportSummaries())
	printView("A final view", mustQuery(o, firstPlan.RunnerID))

	banner(3, "Request B · managed lifecycle", "")
	secondPlan := mustStartRequest(ctx, o, "reply to the customer asking for a delivery ETA", orchestrate.RequestPriorityDefault)
	stopB := mustWatchRunner(o, "B", secondPlan.RunnerID)
	defer stopB()
	mustWaitForPhase(o, secondPlan.RunnerID, runnerpkg.PhaseSettled, 3*time.Second, "B settled")
	runnerB := mustRunner(o, secondPlan.RunnerID)
	printPlan("B plan", secondPlan)
	printValues("B report summaries", runnerB.ReportSummaries())
	printView("B final view", mustQuery(o, secondPlan.RunnerID))

	banner(4, "Request C · highest priority", "")
	thirdPlan := mustStartRequest(ctx, o, "prepare tomorrow morning's production release checklist", orchestrate.RequestPriorityHighest)
	stopC := mustWatchRunner(o, "C", thirdPlan.RunnerID)
	defer stopC()
	mustWaitForPhase(o, thirdPlan.RunnerID, runnerpkg.PhaseSettled, 3*time.Second, "C settled")
	runnerC := mustRunner(o, thirdPlan.RunnerID)
	printPlan("C plan", thirdPlan)
	printValues("C report summaries", runnerC.ReportSummaries())
	printView("C final view", mustQuery(o, thirdPlan.RunnerID))

	time.Sleep(100 * time.Millisecond)
	fmt.Println()
	fmt.Println(green(bold("✓ Demo complete.")) + dim(" all three runners settled through the managed lifecycle."))
}

func configureDemoOrchestrator(ctx context.Context, logger *slog.Logger) (*orchestrate.Orchestrator, func()) {
	o := orchestrate.NewOrchestrator(ctx, logger)
	must(o.SetMaxRunningRunners(1))

	root, err := os.MkdirTemp("", "dango-orchestrate-demo-")
	if err != nil {
		fatalf("mkdir temp: %v", err)
	}
	plannerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		input, err := demoPlannerInputFromOpenAIRequest(r.Body)
		if err != nil {
			fatalf("decode planner request: %v", err)
		}
		plannerRequest, err := demoPlannerRequestFromInput(input)
		if err != nil {
			fatalf("parse planner input: %v", err)
		}
		text, err := demoPlanningOutput(plannerRequest)
		if err != nil {
			fatalf("build planner output: %v", err)
		}
		payload, err := json.Marshal(map[string]any{
			"id":         "r1",
			"object":     "response",
			"created_at": 0,
			"model":      "demo-planner",
			"status":     "completed",
			"output": []map[string]any{{
				"id":     "m1",
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]any{{
					"type":        "output_text",
					"text":        text,
					"annotations": []any{},
				}},
			}},
			"parallel_tool_calls": false,
			"tool_choice":         "auto",
			"tools":               []any{},
		})
		if err != nil {
			fatalf("marshal planner response: %v", err)
		}
		_, _ = w.Write(payload)
	}))
	raw := openai.NewClient(option.WithAPIKey("demo-key"), option.WithBaseURL(plannerServer.URL+"/"))
	plannerClient, err := llm.NewClient(llm.ClientConfig{Provider: llm.ProviderOpenAI, Model: "demo-planner", Raw: raw})
	if err != nil {
		fatalf("NewClient(demo planner): %v", err)
	}
	boundPlanner, err := orchestrate.NewEmbeddedOrchestratorSkill(plannerClient, nil, nil)
	if err != nil {
		fatalf("bind demo orchestrator skill: %v", err)
	}
	must(o.SetOrchestratorSkill(boundPlanner))
	for _, spec := range []struct {
		name        string
		description string
	}{
		{name: "collect", description: "Gather notes, facts, and source material."},
		{name: "review", description: "Check clarity, tone, and missing context."},
		{name: "summarize", description: "Write the final message the reader will see."},
	} {
		dir := filepath.Join(root, spec.name)
		must(os.MkdirAll(dir, 0o755))
		content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\nDemo skill body.\n", spec.name, spec.description)
		must(os.WriteFile(filepath.Join(dir, llm.SkillFile), []byte(content), 0o644))
		sk, err := llm.NewSkill(dir, nil, nil)
		must(err)
		must(o.AddSkills(orchestrate.AddSkillConfig{Skill: sk, Client: plannerClient}))
	}
	return o, func() {
		plannerServer.Close()
		_ = os.RemoveAll(root)
	}
}

func demoPlannerInputFromOpenAIRequest(r io.Reader) (string, error) {
	var req struct {
		Input json.RawMessage `json:"input"`
	}
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		return "", err
	}
	return demoPlannerInputFromResponsesInput(req.Input)
}

func demoPlannerInputFromResponsesInput(raw json.RawMessage) (string, error) {
	if strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
		return "", fmt.Errorf("missing input")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}

	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Text    string          `json:"text"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", err
	}
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Role != "" && items[i].Role != "user" {
			continue
		}
		if text, ok := demoPlannerTextFromContent(items[i].Content); ok {
			return text, nil
		}
		if items[i].Text != "" {
			return items[i].Text, nil
		}
	}
	return "", fmt.Errorf("responses input did not contain user text")
}

func demoPlannerTextFromContent(raw json.RawMessage) (string, bool) {
	if strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
		return "", false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, true
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", false
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	if len(texts) == 0 {
		return "", false
	}
	return strings.Join(texts, "\n"), true
}

func demoPlannerRequestFromInput(input string) (*struct {
	Mode string `json:"mode"`
	Task string `json:"task"`
	Data struct {
		Request string `json:"request"`
		Skills  []struct {
			Name string `json:"name"`
		} `json:"skills"`
	} `json:"data"`
}, error) {
	var req struct {
		Mode string `json:"mode"`
		Task string `json:"task"`
		Data struct {
			Request string `json:"request"`
			Skills  []struct {
				Name string `json:"name"`
			} `json:"skills"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return nil, err
	}
	return &req, nil
}

func demoPlanningOutput(req *struct {
	Mode string `json:"mode"`
	Task string `json:"task"`
	Data struct {
		Request string `json:"request"`
		Skills  []struct {
			Name string `json:"name"`
		} `json:"skills"`
	} `json:"data"`
}) (string, error) {
	if req.Mode == "review" {
		buf, err := json.Marshal(map[string]any{"approved": true})
		if err != nil {
			return "", err
		}
		return string(buf), nil
	}
	available := make(map[string]struct{}, len(req.Data.Skills))
	for _, sk := range req.Data.Skills {
		available[sk.Name] = struct{}{}
	}
	missing := make([]string, 0, 2)
	for _, name := range []string{"collect", "summarize"} {
		if _, ok := available[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		buf, err := json.Marshal(map[string]any{"reject": map[string]any{
			"summary":        "demo skills missing",
			"analysis":       "the orchestrator demo requires collect and summarize skills to assemble the base plan",
			"missing_skills": missing,
		}})
		if err != nil {
			return "", err
		}
		return string(buf), nil
	}
	buf, err := json.Marshal(map[string]any{"plan": orchestrate.CoarsePlan{
		Request: req.Data.Request,
		Nodes: []orchestrate.CoarsePlanNode{
			{
				ID:              "collect",
				SkillName:       "collect",
				TaskDescription: "Gather the facts, source notes, and context needed for: " + req.Data.Request,
			},
			{
				ID:              "deliver",
				SkillName:       "summarize",
				TaskDescription: "Write the final reader-facing draft for: " + req.Data.Request,
				DependsOn:       []string{"collect"},
			},
		},
	}})
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func revisedPlan(request string) *orchestrate.CoarsePlan {
	return &orchestrate.CoarsePlan{
		Request: request,
		Nodes: []orchestrate.CoarsePlanNode{
			{
				ID:              "collect",
				SkillName:       "collect",
				TaskDescription: "Gather the facts, source notes, and context needed for: " + request,
			},
			{
				ID:              "review",
				SkillName:       "review",
				TaskDescription: "Check tone, completeness, and next steps before sending.",
				DependsOn:       []string{"collect"},
			},
			{
				ID:              "deliver",
				SkillName:       "summarize",
				TaskDescription: "Write the final reader-facing draft for: " + request,
				DependsOn:       []string{"review"},
			},
		},
	}
}

func missingSkills(skills map[string]*llm.Skill, required ...string) []string {
	missing := make([]string, 0, len(required))
	for _, name := range required {
		if skills[name] == nil {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func mustStartRequest(ctx context.Context, o *orchestrate.Orchestrator, input string, priority orchestrate.RequestPriority) *orchestrate.CoarsePlan {
	runnerID, err := o.StartRequest(ctx, &orchestrate.Request{Input: input, Priority: priority})
	if err != nil {
		fatalf("StartRequest(%q): %v", input, err)
	}
	view, err := o.QueryRunner(runnerID)
	if err != nil {
		fatalf("QueryRunner(%q): %v", runnerID, err)
	}
	if view.Plan == nil {
		fatalf("StartRequest(%q) returned runner %q without a plan", input, runnerID)
	}
	return view.Plan
}

func mustRunner(o *orchestrate.Orchestrator, id string) *runnerpkg.Runner {
	runner, err := o.Runner(id)
	if err != nil {
		fatalf("Runner(%q): %v", id, err)
	}
	return runner
}

func mustQuery(o *orchestrate.Orchestrator, id string) *runnerpkg.RunnerView {
	view, err := o.QueryRunner(id)
	if err != nil {
		fatalf("QueryRunner(%q): %v", id, err)
	}
	return view
}

func mustWatchRunner(o *orchestrate.Orchestrator, label, id string) func() {
	updates, unsubscribe, err := o.SubscribeRunner(id, 32)
	if err != nil {
		fatalf("SubscribeRunner(%q): %v", id, err)
	}
	tag := runnerTag(label)
	go func() {
		var lastPhase runnerpkg.RunnerPhase
		var lastStatus runnerpkg.RunnerStatus
		for update := range updates {
			if update.Event == nil && update.Phase == lastPhase && update.State.Status == lastStatus {
				continue
			}
			lastPhase = update.Phase
			lastStatus = update.State.Status
			phase := colorPhase(update.Phase)
			status := colorStatus(update.State.Status)
			if update.Event == nil {
				fmt.Printf("    %s %s%s  %s%s\n",
					tag,
					dim("phase="), phase,
					dim("status="), status)
				continue
			}
			event := colorEvent(update.Event.Type.String())
			node := update.Event.NodeID
			if node == "" {
				node = "-"
			}
			fmt.Printf("    %s %s%s  %s%s  %s%s  %s%s  %s%v\n",
				tag,
				dim("phase="), phase,
				dim("status="), status,
				dim("event="), event,
				dim("node="), bold(node),
				dim("data="), update.Event.Data,
			)
		}
		fmt.Printf("    %s %s\n", tag, dim("stream closed"))
	}()
	return unsubscribe
}

func mustInstallRunnerBehavior(o *orchestrate.Orchestrator, runnerID string, label string, deliverGate <-chan struct{}) {
	runner := mustRunner(o, runnerID)
	for id, node := range runner.Nodes() {
		executor, ok := node.Executor.(*orchestrate.Executor)
		if !ok || executor == nil {
			fatalf("runner %q node %q executor = %T, want *orchestrate.Executor", runnerID, id, node.Executor)
		}
		nodeID := id
		executor.RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*runnerpkg.Node, error) {
			switch nodeID {
			case "collect":
				if err := sleepOrCancel(ctx, 120*time.Millisecond); err != nil {
					return nil, nil, err
				}
				return fmt.Sprintf("%s gathered the source notes", label), nil, nil
			case "review":
				if err := sleepOrCancel(ctx, 100*time.Millisecond); err != nil {
					return nil, nil, err
				}
				return fmt.Sprintf("%s reviewed %v for clarity and tone", label, parentOutputs["collect"]), nil, nil
			case "deliver":
				if deliverGate != nil {
					select {
					case <-ctx.Done():
						return nil, nil, ctx.Err()
					case <-deliverGate:
					}
				}
				if err := sleepOrCancel(ctx, 160*time.Millisecond); err != nil {
					return nil, nil, err
				}
				inputKey := "collect"
				if _, ok := parentOutputs["review"]; ok {
					inputKey = "review"
				}
				return fmt.Sprintf("%s sent the final draft based on %v", label, parentOutputs[inputKey]), nil, nil
			default:
				if err := sleepOrCancel(ctx, 80*time.Millisecond); err != nil {
					return nil, nil, err
				}
				return fmt.Sprintf("%s finished %s", label, nodeID), nil, nil
			}
		}
	}
}

func sleepOrCancel(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func mustWaitForPhase(o *orchestrate.Orchestrator, id string, phase runnerpkg.RunnerPhase, timeout time.Duration, label string) *runnerpkg.RunnerView {
	return mustWaitForView(o, id, timeout, label, func(view *runnerpkg.RunnerView) bool {
		return view.Phase == phase
	})
}

func mustWaitForStatus(o *orchestrate.Orchestrator, id string, status runnerpkg.RunnerStatus, timeout time.Duration, label string) *runnerpkg.RunnerView {
	return mustWaitForView(o, id, timeout, label, func(view *runnerpkg.RunnerView) bool {
		return view.State.Status == status
	})
}

func mustWaitForView(o *orchestrate.Orchestrator, id string, timeout time.Duration, label string, predicate func(*runnerpkg.RunnerView) bool) *runnerpkg.RunnerView {
	deadline := time.Now().Add(timeout)
	var lastView *runnerpkg.RunnerView
	for time.Now().Before(deadline) {
		view := mustQuery(o, id)
		lastView = view
		if predicate(view) {
			return view
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lastView == nil {
		fatalf("timed out waiting for %s", label)
	}
	fatalf("timed out waiting for %s (last phase=%s status=%s)", label, lastView.Phase, lastView.State.Status)
	return nil
}

func printPlan(label string, plan *orchestrate.CoarsePlan) {
	fmt.Println("  " + bold(label) + dim(":"))
	for _, node := range plan.Nodes {
		deps := "∅"
		if len(node.DependsOn) > 0 {
			deps = strings.Join(node.DependsOn, ",")
		}
		fmt.Printf("    %s %s %s  %s%s  %s%s\n",
			dim("•"),
			bold(cyan(node.ID)),
			dim("("+node.SkillName+")"),
			dim("deps="), deps,
			dim("task="), node.TaskDescription,
		)
	}
}

func printValues(label string, values map[string]any) {
	fmt.Println("  " + bold(label) + dim(":"))
	if len(values) == 0 {
		fmt.Println("    " + dim("(none)"))
		return
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("    %s %s %s %s\n", dim("•"), bold(cyan(key)), dim("→"), formatDemoValue(values[key]))
	}
}

func formatDemoValue(value any) string {
	switch v := value.(type) {
	case orchestrate.ExecutionPlanner:
		return "plan: " + v.TaskDescription
	case *orchestrate.ExecutionPlanner:
		if v == nil {
			return "<nil>"
		}
		return "plan: " + v.TaskDescription
	default:
		return fmt.Sprint(value)
	}
}

func printView(label string, view *runnerpkg.RunnerView) {
	fmt.Printf("  %s runner=%s  %s%s  %s%s  %s%s\n",
		bold(label)+dim(":"),
		bold(view.RunnerID),
		dim("phase="), colorPhase(view.Phase),
		dim("status="), colorStatus(view.State.Status),
		dim("completed="), bold(fmt.Sprintf("%d", len(view.Snapshot.CompletedNodes))),
	)
}

func must(err error) {
	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, red("error: ")+format+"\n", args...)
	os.Exit(1)
}
