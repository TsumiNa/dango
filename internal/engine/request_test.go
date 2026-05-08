package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
)

func TestRequestPriorityValid(t *testing.T) {
	tests := []struct {
		name     string
		priority RequestPriority
		want     bool
	}{
		{name: "default zero value", priority: RequestPriorityDefault, want: true},
		{name: "highest", priority: RequestPriorityHighest, want: true},
		{name: "middle", priority: 2, want: true},
		{name: "below range", priority: -1, want: false},
		{name: "above range", priority: RequestPriorityHighest + 1, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.priority.valid(); got != tt.want {
				t.Fatalf("priority.valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStartRequest_StreamsRejectedRequestWithoutPlanner(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	resp, err := o.StartRequest(context.Background(), Request{Input: "summarize this repository"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	if resp == nil || resp.Stream == nil {
		t.Fatal("StartRequest response stream is nil")
	}
	if resp.RequestID == "" {
		t.Fatal("StartRequest returned empty requestID")
	}
	failedEvent := mustReadOrchestratorFailureEvent(t, resp.Stream)
	if failedEvent.Scope.RequestID != resp.RequestID {
		t.Fatalf("failure scope.request_id = %q, want %q", failedEvent.Scope.RequestID, resp.RequestID)
	}
	var failed string
	_ = json.Unmarshal(failedEvent.Delta, &failed)
	if failed == "" {
		failed = string(failedEvent.Delta)
	}
	if !strings.Contains(failed, "request rejected") {
		t.Fatalf("failure = %q, want request rejection", failed)
	}
	if len(o.Runners()) != 0 {
		t.Fatalf("expected no runners to be created on rejection")
	}
}

func TestStartRequest_BuildsRunnerFromPlanAndReturnsID(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	perSkillClient := &llm.Client{}
	executeClient := &llm.Client{}
	store := mustNewRunnerStore(t, t.TempDir())
	if err := o.SetRunnerStore(store); err != nil {
		t.Fatalf("SetRunnerStore: %v", err)
	}
	mustAddSkills(t, o,
		newTestSkillRegistration(t, "plan", "Draft a plan.", perSkillClient),
		newTestSkillRegistration(t, "execute", "Execute a plan.", executeClient),
	)
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t,
		mustPlanJSON(t, &CoarsePlan{
			Request: "build a report",
			Nodes: []CoarsePlanNode{
				{ID: "draft", SkillName: "plan", TaskDescription: "Draft the execution outline."},
				{ID: "run", SkillName: "execute", TaskDescription: "Execute the approved outline.", DependsOn: []string{"draft"}},
			},
		}),
		mustReviewJSON(t, true, ""),
	)); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	artifactsDir := t.TempDir()
	resp, err := o.StartRequest(context.Background(), Request{Input: "build a report", ArtifactsDir: artifactsDir})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	runnerID := mustReadRunnerCreated(t, resp.Stream)
	if runnerID == "" {
		t.Fatal("runnerID is empty")
	}
	if resp.Stream == nil {
		t.Fatal("response stream is nil")
	}

	managedRunner := o.Runners()[runnerID]
	if managedRunner == nil {
		t.Fatalf("expected runner %q to be stored", runnerID)
	}
	if managedRunner.ID() != runnerID {
		t.Errorf("Runner.ID() = %q, want %q", managedRunner.ID(), runnerID)
	}
	if managedRunner.PlannerSkill() == nil {
		t.Fatal("expected runner planner skill to be configured")
	}
	if len(managedRunner.Nodes()) != 2 {
		t.Fatalf("len(Nodes) = %d, want 2", len(managedRunner.Nodes()))
	}

	draft := managedRunner.Nodes()["draft"]
	run := managedRunner.Nodes()["run"]
	if draft == nil || run == nil {
		t.Fatalf("expected draft and run nodes to exist, got draft=%v run=%v", draft, run)
	}
	draftExecutor := mustNodeExecutor(t, draft)
	if draftExecutor.Skill().Name != "plan" {
		t.Fatalf("draft executor skill = %v, want plan", draftExecutor)
	}
	if got := draftExecutor.LLMClient(); got != perSkillClient {
		t.Fatalf("draft executor LLMClient() = %p, want %p", got, perSkillClient)
	}
	runExecutor := mustNodeExecutor(t, run)
	if runExecutor.Skill().Name != "execute" {
		t.Fatalf("run executor skill = %v, want execute", runExecutor)
	}
	if got := runExecutor.LLMClient(); got != executeClient {
		t.Fatalf("run executor LLMClient() = %p, want %p", got, executeClient)
	}
	if len(run.Parents) != 1 || run.Parents[0].Id != "draft" {
		t.Fatalf("run parents = %+v, want [draft]", run.Parents)
	}
	if got := runExecutor.Planner().TaskDescription; got != "Execute the approved outline." {
		t.Errorf("run task description = %q, want %q", got, "Execute the approved outline.")
	}
	if got := draftExecutor.Planner().SourceInput; got != "build a report" {
		t.Errorf("draft source input = %q, want original request", got)
	}
	if got := runExecutor.Planner().SourceInput; got != "build a report" {
		t.Errorf("run source input = %q, want original request", got)
	}
	if got := runExecutor.Planner().ArtifactsDir; got != artifactsDir {
		t.Errorf("run artifacts dir = %q, want %q", got, artifactsDir)
	}
	realArtifactsDir, err := filepath.EvalSymlinks(artifactsDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(artifactsDir): %v", err)
	}
	if got := runExecutor.Skill().AccessibleDirs(); len(got) != 1 || got[0] != realArtifactsDir {
		t.Fatalf("run skill accessible dirs = %v, want [%s]", got, realArtifactsDir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := managedRunner.Wait(ctx); err != nil {
		t.Fatalf("runner Wait: %v", err)
	}
	view, err := o.QueryRunner(runnerID)
	if err != nil {
		t.Fatalf("QueryRunner: %v", err)
	}
	if view.Phase != PhaseSettled {
		t.Fatalf("final phase = %q, want settled", view.Phase)
	}
}

func TestStartRequest_ReturnsReplayableRequestStream(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o, newTestSkillRegistration(t, "single", "Single-step runner.", nil))
	planOutput := mustPlanJSON(t, &CoarsePlan{
		Request: "run a single node",
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node.",
		}},
	})
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, planOutput, mustReviewJSON(t, true, ""))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	resp, err := o.StartRequest(context.Background(), Request{Input: "run a single node"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	if resp == nil || resp.Stream == nil {
		t.Fatal("StartRequest response stream is nil")
	}

	sub, err := resp.Stream.Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(64))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	deadline := time.Now().Add(2 * time.Second)
	var sawPlanText, sawCompletedStatus bool
	for time.Now().Before(deadline) && !(sawPlanText && sawCompletedStatus) {
		readCtx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		event, ok, err := sub.Next(readCtx)
		cancel()
		if err != nil || !ok {
			break
		}
		if event.From.Layer != "orchestrator" {
			continue
		}
		var delta string
		if jsonErr := json.Unmarshal(event.Delta, &delta); jsonErr != nil {
			continue
		}
		switch event.EventType {
		case streampkg.EventLLMOutputDelta:
			if delta == planOutput {
				sawPlanText = true
			}
		case streampkg.EventStatusCompleted:
			if delta == "orchestrator planning completed" {
				sawCompletedStatus = true
			}
		}
	}
	if !sawPlanText {
		t.Fatal("missing planner text delta stream event from orchestrator planning")
	}
	if !sawCompletedStatus {
		t.Fatal("missing planning-completed status stream event from orchestrator planning")
	}

	runnerID := mustReadRunnerCreated(t, resp.Stream)
	managedRunner, err := o.Runner(runnerID)
	if err != nil {
		t.Fatalf("Runner: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := managedRunner.Wait(waitCtx); err != nil {
		t.Fatalf("runner Wait: %v", err)
	}
}

func TestStartRequestStreamsBeforeRunnerCreation(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o, newTestSkillRegistration(t, "single", "Single-step runner.", nil))
	planOutput := mustPlanJSON(t, &CoarsePlan{
		Request: "run a single node",
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node.",
		}},
	})
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, planOutput, mustReviewJSON(t, true, ""))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	resp, err := o.StartRequest(context.Background(), Request{Input: "run a single node"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	if resp == nil || resp.Stream == nil {
		t.Fatal("StartRequest returned nil stream")
	}
	if resp.RequestID == "" {
		t.Fatal("StartRequest returned empty requestID")
	}
	if resp.RunnerID != "" {
		t.Fatalf("StartRequest returned runnerID %q before stream synchronization", resp.RunnerID)
	}
	sub, err := resp.Stream.Subscribe(streampkg.Filter{EventTypes: []string{streampkg.EventStatusStarted, streampkg.EventStatusProgress}}, streampkg.WithSubscriberBuffer(16))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	deadline := time.Now().Add(2 * time.Second)
	var sawStarted bool
	var runnerID string
	for time.Now().Before(deadline) && (!sawStarted || runnerID == "") {
		readCtx, cancelRead := context.WithTimeout(context.Background(), time.Until(deadline))
		event, ok, err := sub.Next(readCtx)
		cancelRead()
		if err != nil || !ok {
			t.Fatalf("request stream event ok=%v err=%v", ok, err)
		}
		if event.Scope.RequestID != resp.RequestID {
			t.Fatalf("event scope.request_id = %q, want %q", event.Scope.RequestID, resp.RequestID)
		}
		switch event.EventType {
		case streampkg.EventStatusStarted:
			if event.From.Layer == "orchestrator" {
				sawStarted = true
			}
		case streampkg.EventStatusProgress:
			if event.From.Layer != "orchestrator" {
				continue
			}
			var delta map[string]string
			_ = json.Unmarshal(event.Delta, &delta)
			if delta["message"] == "runner created" && delta["runner_id"] != "" {
				runnerID = delta["runner_id"]
				if event.Scope.RunnerID != runnerID {
					t.Fatalf("runner-created scope.runner_id = %q, want %q", event.Scope.RunnerID, runnerID)
				}
			}
		}
	}
	if !sawStarted {
		t.Fatal("missing orchestrator started event")
	}
	if runnerID == "" {
		t.Fatal("runnerID is empty")
	}
}

func TestStartRequest_ReplayPreservesRequestIdentity(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o, newTestSkillRegistration(t, "single", "Single-step runner.", nil))
	planOutput := mustPlanJSON(t, &CoarsePlan{
		Request: "run a single node",
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node.",
		}},
	})
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, planOutput, mustReviewJSON(t, true, ""))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	resp, err := o.StartRequest(context.Background(), Request{Input: "run a single node"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	if resp.RequestID == "" {
		t.Fatal("StartRequest returned empty requestID")
	}
	runnerID := mustReadRunnerCreated(t, resp.Stream)

	replayed, err := resp.Stream.Replay(streampkg.Filter{}, streampkg.WithReplayFrom(1))
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(replayed) == 0 {
		t.Fatal("Replay returned no events")
	}
	var sawRunnerCreated bool
	for _, event := range replayed {
		if event.Scope.RequestID != resp.RequestID {
			t.Fatalf("replayed scope.request_id = %q, want %q", event.Scope.RequestID, resp.RequestID)
		}
		if event.EventType != streampkg.EventStatusProgress || event.From.Layer != "orchestrator" {
			continue
		}
		var delta map[string]string
		_ = json.Unmarshal(event.Delta, &delta)
		if delta["message"] == "runner created" && delta["runner_id"] == runnerID {
			sawRunnerCreated = true
			if event.Scope.RunnerID != runnerID {
				t.Fatalf("replayed runner-created scope.runner_id = %q, want %q", event.Scope.RunnerID, runnerID)
			}
		}
	}
	if !sawRunnerCreated {
		t.Fatal("replay missing runner-created event")
	}
}

func TestStartRequest_StreamsPlannerReasoningAndPlanningExchange(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o, newTestSkillRegistration(t, "single", "Single-step runner.", nil))
	planOutput := mustPlanJSON(t, &CoarsePlan{
		Request: "run a single node",
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node.",
		}},
	})
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkillWithReasoning(t, "checked the available skills and request routing", planOutput, mustReviewJSON(t, true, ""))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	resp, err := o.StartRequest(context.Background(), Request{Input: "run a single node"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	sub, err := resp.Stream.Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(64))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()

	deadline := time.Now().Add(2 * time.Second)
	var sawReasoning, sawPlanningExchange bool
	for time.Now().Before(deadline) && !(sawReasoning && sawPlanningExchange) {
		readCtx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		event, ok, err := sub.Next(readCtx)
		cancel()
		if err != nil || !ok {
			break
		}
		if event.From.Layer != "orchestrator" {
			continue
		}
		var delta string
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			continue
		}
		switch event.EventType {
		case streampkg.EventLLMReasoningDelta:
			if strings.Contains(delta, "checked the available skills") {
				sawReasoning = true
			}
		case streampkg.EventLLMOutputDelta:
			doc, err := runnerpkg.ParseExchangeMarkdown(delta)
			if err != nil {
				continue
			}
			if doc.Stage == runnerpkg.ExchangeStage("planning") && doc.SkillName == "orchestrator" && doc.TaskDescription == "run a single node" && doc.Handoff == planOutput && strings.Contains(doc.Reasoning, "checked the available skills") {
				sawPlanningExchange = true
			}
		}
	}
	if !sawReasoning {
		t.Fatal("missing planner reasoning stream event from orchestrator planning")
	}
	if !sawPlanningExchange {
		t.Fatal("missing planning exchange markdown stream event from orchestrator planning")
	}

	runnerID := mustReadRunnerCreated(t, resp.Stream)
	managedRunner, err := o.Runner(runnerID)
	if err != nil {
		t.Fatalf("Runner: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := managedRunner.Wait(waitCtx); err != nil {
		t.Fatalf("runner Wait: %v", err)
	}
}

func TestStartRequest_EmitsRequestStreamEvents(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o, newTestSkillRegistration(t, "single", "Single-step runner.", nil))
	planOutput := mustPlanJSON(t, &CoarsePlan{
		Request: "run a single node",
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node.",
		}},
	})
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, planOutput, mustReviewJSON(t, true, ""))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	resp, err := o.StartRequest(context.Background(), Request{Input: "run a single node"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	if resp == nil || resp.Stream == nil {
		t.Fatal("StartRequest response stream is nil")
	}
	sub, err := resp.Stream.Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(64))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Cancel()
	defer resp.Stream.Close()
	var (
		runnerID    string
		sawPlanText bool
		sawCreated  bool
		sawSettled  bool
		sawNodeDone bool
		sawExecutor bool
	)
	readCtx, cancelRead := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRead()
	for !(sawPlanText && sawCreated && sawSettled && sawNodeDone && sawExecutor) {
		event, ok, err := sub.Next(readCtx)
		if err != nil {
			t.Fatalf("request stream event: %v", err)
		}
		if !ok {
			t.Fatalf("request stream closed before expected events: text=%v created=%v settled=%v nodeDone=%v executor=%v",
				sawPlanText, sawCreated, sawSettled, sawNodeDone, sawExecutor)
		}
		if event.Scope.RequestID != resp.RequestID {
			t.Fatalf("event scope.request_id = %q, want %q", event.Scope.RequestID, resp.RequestID)
		}
		var deltaText string
		_ = json.Unmarshal(event.Delta, &deltaText)
		switch event.EventType {
		case streampkg.EventLLMOutputDelta:
			if event.From.Layer == "orchestrator" && event.Status == streampkg.StatusCompleted && deltaText == planOutput {
				sawPlanText = true
			}
		case streampkg.EventStatusProgress:
			if event.From.Layer == "orchestrator" {
				var delta map[string]string
				_ = json.Unmarshal(event.Delta, &delta)
				if delta["message"] == "runner created" && delta["runner_id"] != "" {
					runnerID = delta["runner_id"]
					sawCreated = true
				}
			}
		case streampkg.EventRunnerPhaseChanged:
			var delta map[string]string
			_ = json.Unmarshal(event.Delta, &delta)
			if event.Scope.RunnerID == runnerID && delta["phase"] == string(PhaseSettled) && delta["status"] == "idle" {
				sawSettled = true
			}
		case streampkg.EventRunnerNodeCompleted:
			if event.Scope.RunnerID == runnerID && event.Scope.NodeID == "only" {
				sawNodeDone = true
			}
		case streampkg.EventExecutorExecuteCompleted:
			if event.Scope.RunnerID == runnerID && event.Scope.NodeID == "only" && event.From.Layer == "executor" {
				sawExecutor = true
			}
		}
	}
	managedRunner, err := o.Runner(runnerID)
	if err != nil {
		t.Fatalf("Runner: %v", err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	if err := managedRunner.Wait(waitCtx); err != nil {
		t.Fatalf("runner Wait: %v", err)
	}
	if !sawPlanText || !sawCreated || !sawSettled || !sawNodeDone || !sawExecutor {
		t.Fatalf("missing stream events: text=%v created=%v settled=%v nodeDone=%v executor=%v",
			sawPlanText, sawCreated, sawSettled, sawNodeDone, sawExecutor)
	}
}

func TestStartRequest_CreatesReplayableRunnerStream(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o, newTestSkillRegistration(t, "single", "Single-step runner.", nil))
	planOutput := mustPlanJSON(t, &CoarsePlan{
		Request: "run a single node",
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node.",
		}},
	})
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, planOutput, mustReviewJSON(t, true, ""))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	resp, err := o.StartRequest(context.Background(), Request{Input: "run a single node"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	runnerID := mustReadRunnerCreated(t, resp.Stream)
	managedRunner, err := o.Runner(runnerID)
	if err != nil {
		t.Fatalf("Runner: %v", err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWait()
	if err := managedRunner.Wait(waitCtx); err != nil {
		t.Fatalf("runner Wait: %v", err)
	}

	sub, err := o.SubscribeRunnerStream(runnerID, streampkg.Filter{}, streampkg.WithSubscriberBuffer(64))
	if err != nil {
		t.Fatalf("SubscribeRunnerStream: %v", err)
	}
	defer sub.Cancel()

	var sawSettled, sawNodeDone bool
	readCtx, cancelRead := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRead()
	for !(sawSettled && sawNodeDone) {
		event, ok, err := sub.Next(readCtx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			t.Fatal("stream closed before replaying expected events")
		}
		switch event.EventType {
		case streampkg.EventRunnerPhaseChanged:
			var delta map[string]string
			_ = json.Unmarshal(event.Delta, &delta)
			if event.Scope.RunnerID == runnerID && delta["phase"] == string(PhaseSettled) {
				sawSettled = true
			}
		case streampkg.EventRunnerNodeCompleted:
			if event.Scope.RunnerID == runnerID && event.Scope.NodeID == "only" {
				sawNodeDone = true
			}
		}
	}
}

func TestSubscribeRunnerStream_RejectsUnknownID(t *testing.T) {
	o := newOrchestrator(testLogger)
	if _, err := o.SubscribeRunnerStream("missing", streampkg.Filter{}); !errors.Is(err, ErrRunnerNotFound) {
		t.Fatalf("SubscribeRunnerStream err = %v, want ErrRunnerNotFound", err)
	}
}

func TestStartRequest_StreamsFailureWhenPlanUsesUnknownSkill(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, mustPlanJSON(t, &CoarsePlan{
		Request: "process images",
		Nodes:   []CoarsePlanNode{{ID: "only", SkillName: "missing", TaskDescription: "process images"}},
	}))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	resp, err := o.StartRequest(context.Background(), Request{Input: "process images"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	failed := mustReadOrchestratorFailure(t, resp.Stream)
	if !strings.Contains(failed, "unregistered skill") {
		t.Fatalf("failure = %q, want unregistered skill", failed)
	}
	if resp != nil && resp.RunnerID != "" {
		t.Fatalf("runnerID = %q, want empty", resp.RunnerID)
	}
	if len(o.Runners()) != 0 {
		t.Fatalf("expected no runners to be stored when plan assembly fails")
	}
}

func TestStartRequest_RejectsPriorityOutsideRange(t *testing.T) {
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o, newTestSkillRegistration(t, "single", "Single-step runner.", nil))
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, mustPlanJSON(t, &CoarsePlan{
		Request: "run now",
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node.",
		}},
	}))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	for _, priority := range []RequestPriority{-1, RequestPriorityHighest + 1} {
		resp, err := o.StartRequest(context.Background(), Request{Input: "run now", Priority: priority})
		if err == nil {
			t.Fatalf("expected StartRequest to reject priority %d", priority)
		}
		if resp != nil {
			t.Fatalf("expected nil response for invalid priority %d, got %+v", priority, resp)
		}
		if len(o.Runners()) != 0 {
			t.Fatalf("expected no runners to be created for invalid priority %d", priority)
		}
	}
}
