package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
)

func TestStartRequest_ReturnsRejectedErrorWithoutPlanner(t *testing.T) {
	o := newOrchestrator(testLogger)
	_, err := o.StartRequest(context.Background(), &Request{Input: "summarize this repository"})
	var rejected *RequestRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("StartRequest err = %v, want RequestRejectedError", err)
	}
	if rejected.Reason == nil || rejected.Reason.Summary == "" || rejected.Reason.Analysis == "" {
		t.Fatalf("rejected reason = %+v, want populated summary and analysis", rejected.Reason)
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
		newTestSkillConfig(t, "plan", "Draft a plan.", perSkillClient),
		newTestSkillConfig(t, "execute", "Execute a plan.", executeClient),
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
	runnerID, err := o.StartRequest(context.Background(), &Request{Input: "build a report", ArtifactsDir: artifactsDir})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	if runnerID == "" {
		t.Fatal("runnerID is empty")
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
	if got := runExecutor.Planner().ArtifactsDir; got != artifactsDir {
		t.Errorf("run artifacts dir = %q, want %q", got, artifactsDir)
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

func TestStartRequestWithProgress_StreamsPlannerDeltas(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o, newTestSkillConfig(t, "single", "Single-step runner.", nil))
	planOutput := mustPlanJSON(t, &CoarsePlan{
		Request: "run a single node",
		Nodes: []CoarsePlanNode{{
			ID:              "only",
			SkillName:       "single",
			TaskDescription: "Run the only node.",
		}},
	})
	if err := o.SetOrchestratorSkill(bindStreamingTestOrchestratorSkill(t, planOutput, mustReviewJSON(t, true, ""))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	var events []OrchestratorProgressEvent
	runnerID, err := o.StartRequestWithProgress(context.Background(), &Request{Input: "run a single node"}, func(event OrchestratorProgressEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("StartRequestWithProgress: %v", err)
	}
	if runnerID == "" {
		t.Fatal("runnerID is empty")
	}

	var sawReasoning bool
	var sawPlanText bool
	var sawCompletedStatus bool
	for _, event := range events {
		if event.Type == OrchestratorProgressReasoning && event.Delta == "planning stream is active" {
			sawReasoning = true
		}
		if event.Type == OrchestratorProgressText && event.Delta == planOutput {
			sawPlanText = true
		}
		if event.Type == OrchestratorProgressStatus && event.Message == "orchestrator planning stream completed" {
			sawCompletedStatus = true
		}
	}
	if !sawReasoning {
		t.Fatalf("missing reasoning progress event: %+v", events)
	}
	if !sawPlanText {
		t.Fatalf("missing planner text progress event: %+v", events)
	}
	if !sawCompletedStatus {
		t.Fatalf("missing completed status event: %+v", events)
	}

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
	mustAddSkills(t, o, newTestSkillConfig(t, "single", "Single-step runner.", nil))
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

	eventStream := streampkg.New(streampkg.Scope{RequestID: "req_test"})
	sub, err := eventStream.Subscribe(streampkg.Filter{}, streampkg.WithSubscriberBuffer(64))
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	runnerID, err := o.StartRequest(context.Background(), &Request{Input: "run a single node", Stream: eventStream})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
	}
	managedRunner, err := o.Runner(runnerID)
	if err != nil {
		t.Fatalf("Runner: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := managedRunner.Wait(waitCtx); err != nil {
		t.Fatalf("runner Wait: %v", err)
	}
	eventStream.Close()

	var (
		sawPlanText bool
		sawCreated  bool
		sawSettled  bool
		sawNodeDone bool
		sawExecutor bool
	)
	for event := range sub.Events() {
		var deltaText string
		_ = json.Unmarshal(event.Delta, &deltaText)
		switch event.EventType {
		case streampkg.EventLLMOutputDelta:
			if event.From.Layer == "orchestrator" && event.Status == streampkg.StatusCompleted && deltaText == planOutput {
				sawPlanText = true
			}
		case streampkg.EventStatusProgress:
			if event.From.Layer == "orchestrator" && event.Scope.RunnerID == runnerID {
				sawCreated = true
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
	if !sawPlanText || !sawCreated || !sawSettled || !sawNodeDone || !sawExecutor {
		t.Fatalf("missing stream events: text=%v created=%v settled=%v nodeDone=%v executor=%v",
			sawPlanText, sawCreated, sawSettled, sawNodeDone, sawExecutor)
	}
}

func TestStartRequest_CreatesReplayableRunnerStream(t *testing.T) {
	clearLLMEnv(t)
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o, newTestSkillConfig(t, "single", "Single-step runner.", nil))
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

	runnerID, err := o.StartRequest(context.Background(), &Request{Input: "run a single node"})
	if err != nil {
		t.Fatalf("StartRequest: %v", err)
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

	sub, err := o.SubscribeRunnerStream(runnerID, streampkg.Filter{}, streampkg.WithSubscriberBuffer(64))
	if err != nil {
		t.Fatalf("SubscribeRunnerStream: %v", err)
	}
	defer sub.Cancel()

	var sawCreated, sawSettled, sawNodeDone bool
	readCtx, cancelRead := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRead()
	for !(sawCreated && sawSettled && sawNodeDone) {
		event, ok, err := sub.Next(readCtx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			t.Fatal("stream closed before replaying expected events")
		}
		switch event.EventType {
		case streampkg.EventStatusProgress:
			if event.From.Layer == "orchestrator" && event.Scope.RunnerID == runnerID {
				sawCreated = true
			}
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

func TestStartRequest_ErrorsWhenPlanUsesUnknownSkill(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetOrchestratorSkill(bindTestOrchestratorSkill(t, mustPlanJSON(t, &CoarsePlan{
		Request: "process images",
		Nodes:   []CoarsePlanNode{{ID: "only", SkillName: "missing", TaskDescription: "process images"}},
	}))); err != nil {
		t.Fatalf("SetOrchestratorSkill(test planner): %v", err)
	}

	runnerID, err := o.StartRequest(context.Background(), &Request{Input: "process images"})
	if err == nil {
		t.Fatal("expected error when the plan references an unknown skill")
	}
	if runnerID != "" {
		t.Fatalf("runnerID = %q, want empty", runnerID)
	}
	if len(o.Runners()) != 0 {
		t.Fatalf("expected no runners to be stored when plan assembly fails")
	}
}

func TestStartRequest_RejectsPriorityOutsideRange(t *testing.T) {
	o := newOrchestrator(testLogger)
	mustAddSkills(t, o, newTestSkillConfig(t, "single", "Single-step runner.", nil))
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
		runnerID, err := o.StartRequest(context.Background(), &Request{Input: "run now", Priority: priority})
		if err == nil {
			t.Fatalf("expected StartRequest to reject priority %d", priority)
		}
		if runnerID != "" {
			t.Fatalf("expected empty runnerID for invalid priority %d, got %q", priority, runnerID)
		}
		if len(o.Runners()) != 0 {
			t.Fatalf("expected no runners to be created for invalid priority %d", priority)
		}
	}
}
