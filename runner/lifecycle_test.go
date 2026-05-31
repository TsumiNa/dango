package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	streampkg "github.com/tsumina/dango/stream"
)

func newPolishNode(id string, polishFrag any) *Node {
	return &Node{
		Id: id,
		Agent: &testAgent{
			polish: func(ctx context.Context) (any, error) {
				return polishFrag, nil
			},
		},
	}
}

func TestRunner_StartPolishRequiresPlan(t *testing.T) {
	r := newTestRunner()
	if err := r.StartPolish(context.Background()); !errors.Is(err, ErrPlanRequired) {
		t.Fatalf("StartPolish without plan err = %v, want ErrPlanRequired", err)
	}
}

func TestRunner_StartManagedReviewsExecutesReportsAndSettles(t *testing.T) {
	plan := &CoarsePlan{
		Request: "demo",
		Nodes:   []CoarsePlanNode{{ID: "A", SkillName: "single", TaskDescription: "demo"}},
	}
	nodes := map[string]*Node{
		"A": {
			Id: "A",
			Agent: &testAgent{
				polish: func(ctx context.Context) (any, error) { return "frag-A", nil },
				run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
					return "exec-A", nil, nil
				},
				report: func(ctx context.Context, output any) (any, error) {
					return "sum-" + output.(string), nil
				},
			},
		},
	}
	r := New(
		WithLogger(testLogger),
		WithInitialPlan(plan, nodes),
		WithPlannerSkill(bindTestPlannerSkill(t, mustReviewJSON(t, true, ""))),
		WithSkillSummaries([]SkillSummary{{Name: "single", Description: "Single-step skill."}}),
		WithPlanNodeBuilder(func(plan *CoarsePlan) (map[string]*Node, error) { return nodes, nil }),
	)

	if err := r.StartManaged(context.Background()); err != nil {
		t.Fatalf("StartManaged: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := r.Phase(); got != PhaseSettled {
		t.Fatalf("phase = %q, want settled", got)
	}
	if got := r.ReportSummaries()["A"]; got != "sum-exec-A" {
		t.Fatalf("report summary = %v, want sum-exec-A", got)
	}
}

func TestRunner_StartPolishCollectsFragmentsAndAwaitsReview(t *testing.T) {
	plan := &CoarsePlan{Request: "demo"}
	nodes := map[string]*Node{
		"A": newPolishNode("A", "frag-A"),
		"B": newPolishNode("B", "frag-B"),
	}
	r := newTestRunnerForPlan(plan, nodes)

	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached AwaitingReview, got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}

	frags := r.PolishFragments()
	if frags["A"] != "frag-A" {
		t.Errorf("fragment A = %v, want frag-A", frags["A"])
	}
	if frags["B"] != "frag-B" {
		t.Errorf("fragment B = %v, want frag-B", frags["B"])
	}
}

func TestRunner_StartPolishFailureAborts(t *testing.T) {
	plan := &CoarsePlan{Request: "demo"}
	polishErr := errors.New("boom")
	nodes := map[string]*Node{
		"A": {
			Id: "A",
			Agent: &testAgent{
				polish: func(ctx context.Context) (any, error) {
					return nil, polishErr
				},
			},
		},
	}
	r := newTestRunnerForPlan(plan, nodes)

	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := r.Wait(waitCtx)
	if !errors.Is(err, polishErr) {
		t.Fatalf("Wait err = %v, want contains polishErr", err)
	}
	if r.Phase() != PhaseSettled {
		t.Fatalf("phase = %q, want PhaseSettled", r.Phase())
	}
}

func TestRunnerFanOutPolishSkipsCompletedEventWhenMemoSnapshotFails(t *testing.T) {
	r := newTestRunner()
	workspace, err := ProvisionWorkspace(t.TempDir(), r.ID(), []string{"A"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	r.workspace = workspace
	makeUnreadableMemoSnapshotDir(t, workspace, "A", "polish")
	sub, err := r.SubscribeStream(streampkg.Filter{
		EventTypes: []string{streampkg.EventAgentPolishCompleted},
		Scope:      streampkg.Scope{RunnerID: r.ID(), NodeID: "A"},
	}, streampkg.WithSubscriberBuffer(4))
	if err != nil {
		t.Fatalf("SubscribeStream(polish completed): %v", err)
	}
	defer sub.Cancel()

	if _, err := r.fanOutPolish(context.Background(), map[string]*Node{"A": newPolishNode("A", "frag-A")}); err == nil {
		t.Fatal("fanOutPolish error = nil, want memo snapshot failure")
	}
	assertNoStreamEventWithin(t, sub, 100*time.Millisecond)
}

func TestRunnerFanOutReportSkipsCompletedEventWhenMemoSnapshotFails(t *testing.T) {
	r := newTestRunner()
	workspace, err := ProvisionWorkspace(t.TempDir(), r.ID(), []string{"A"}, nil)
	if err != nil {
		t.Fatalf("ProvisionWorkspace: %v", err)
	}
	r.workspace = workspace
	makeUnreadableMemoSnapshotDir(t, workspace, "A", "report")
	sub, err := r.SubscribeStream(streampkg.Filter{
		EventTypes: []string{streampkg.EventAgentReportCompleted},
		Scope:      streampkg.Scope{RunnerID: r.ID(), NodeID: "A"},
	}, streampkg.WithSubscriberBuffer(4))
	if err != nil {
		t.Fatalf("SubscribeStream(report completed): %v", err)
	}
	defer sub.Cancel()

	nodes := map[string]*Node{
		"A": {
			Id: "A",
			Agent: &testAgent{
				report: func(ctx context.Context, output any) (any, error) {
					return "sum-" + output.(string), nil
				},
			},
		},
	}
	if _, err := r.fanOutReport(context.Background(), nodes, map[string]any{"A": "exec-A"}); err == nil {
		t.Fatal("fanOutReport error = nil, want memo snapshot failure")
	}
	assertNoStreamEventWithin(t, sub, 100*time.Millisecond)
}

func TestRunner_AcceptPolishedPlanRunsEngineAndReports(t *testing.T) {
	plan := &CoarsePlan{Request: "demo"}
	var reportCalls atomic.Int32
	nodes := map[string]*Node{
		"A": {
			Id: "A",
			Agent: &testAgent{
				run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
					return "exec-A", nil, nil
				},
				polish: func(ctx context.Context) (any, error) { return "frag-A", nil },
				report: func(ctx context.Context, output any) (any, error) {
					reportCalls.Add(1)
					return "sum-" + output.(string), nil
				},
			},
		},
	}
	r := newTestRunnerForPlan(plan, nodes)

	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}

	// Wait for AwaitingReview.
	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached AwaitingReview, got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}

	acceptedPlan := &CoarsePlan{Request: "reviewed-plan"}
	if err := r.AcceptPolishedPlan(context.Background(), acceptedPlan); err != nil {
		t.Fatalf("AcceptPolishedPlan: %v", err)
	}

	// Wait until engine reports idle, then Complete to run Report.
	waitForRunnerEvent(t, r, EventEngineIdle, "")

	if err := r.Complete(context.Background()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := reportCalls.Load(); got != 1 {
		t.Fatalf("report calls = %d, want 1", got)
	}
	if got := r.Plan().Request; got != acceptedPlan.Request {
		t.Fatalf("accepted plan request = %q, want %q", got, acceptedPlan.Request)
	}
	if got := r.Plan().RunnerID; got != r.ID() {
		t.Fatalf("accepted plan runner id = %q, want %q", got, r.ID())
	}
	if sum := r.ReportSummaries()["A"]; sum != "sum-exec-A" {
		t.Fatalf("summary A = %v, want sum-exec-A", sum)
	}
	if r.Phase() != PhaseSettled {
		t.Fatalf("phase = %q, want PhaseSettled", r.Phase())
	}
}

func makeUnreadableMemoSnapshotDir(t *testing.T, workspace *Workspace, nodeID string, stage string) {
	t.Helper()
	stageRoot := filepath.Join(workspace.ArchiveDir(), "memo", nodeID, stage)
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", stageRoot, err)
	}
	if err := os.WriteFile(filepath.Join(stageRoot, "plan.md.memo.md"), []byte("memo body"), 0o644); err != nil {
		t.Fatalf("WriteFile(snapshot): %v", err)
	}
	if err := os.Chmod(stageRoot, 0o000); err != nil {
		t.Fatalf("Chmod(%s): %v", stageRoot, err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(stageRoot, 0o755)
	})
}

func assertNoStreamEventWithin(t *testing.T, sub *streampkg.Subscription, timeout time.Duration) {
	t.Helper()
	readCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, ok, err := sub.Next(readCtx)
	if !errors.Is(err, context.DeadlineExceeded) || ok {
		t.Fatalf("Next completed event = %v/%v, want no event", ok, err)
	}
}

func TestRunner_RejectPolishedPlanTransitionsToAwaitingReplan(t *testing.T) {
	plan := &CoarsePlan{Request: "demo"}
	nodes := map[string]*Node{
		"A": newPolishNode("A", "frag-A"),
	}
	r := newTestRunnerForPlan(plan, nodes)

	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached AwaitingReview, got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := r.RejectPolishedPlan("needs-revision"); err != nil {
		t.Fatalf("RejectPolishedPlan: %v", err)
	}
	if r.Phase() != PhaseAwaitingReplan {
		t.Fatalf("phase = %q, want PhaseAwaitingReplan", r.Phase())
	}
	if got := r.ReplanReason(); got != "needs-revision" {
		t.Fatalf("ReplanReason = %q, want needs-revision", got)
	}
}

func TestRunner_ReplanWithRestartsPolish(t *testing.T) {
	plan1 := &CoarsePlan{Request: "v1"}
	plan2 := &CoarsePlan{Request: "v2"}
	nodes1 := map[string]*Node{"A": newPolishNode("A", "frag-A1")}
	nodes2 := map[string]*Node{"B": newPolishNode("B", "frag-B2")}

	r := newTestRunnerForPlan(plan1, nodes1)
	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached AwaitingReview (1), got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.RejectPolishedPlan("try-again"); err != nil {
		t.Fatalf("RejectPolishedPlan: %v", err)
	}
	if err := r.ReplanWith(context.Background(), plan2, nodes2); err != nil {
		t.Fatalf("ReplanWith: %v", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached AwaitingReview (2), got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}
	frags := r.PolishFragments()
	if _, ok := frags["A"]; ok {
		t.Fatalf("old fragment A should be cleared, got %v", frags)
	}
	if frags["B"] != "frag-B2" {
		t.Fatalf("fragment B = %v, want frag-B2", frags["B"])
	}
	plan2.Request = "mutated-after-replan"
	if got := r.Plan(); got == nil || got.Request != "v2" || got == plan2 {
		t.Fatalf("Plan() = %#v, want cloned plan with request v2", got)
	}
	nodesView := r.Nodes()
	delete(nodesView, "B")
	if got := r.Nodes()["B"]; got == nil {
		t.Fatalf("Nodes() should return a cloned map")
	}
}

func TestRunner_AcceptRejectConcurrentSingleWinner(t *testing.T) {
	plan := &CoarsePlan{Request: "demo"}
	release := make(chan struct{})
	nodes := map[string]*Node{
		"A": {
			Id: "A",
			Agent: &testAgent{
				run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
					<-release
					return "exec-A", nil, nil
				},
				polish: func(ctx context.Context) (any, error) { return "frag-A", nil },
				report: func(ctx context.Context, output any) (any, error) { return output, nil },
			},
		},
	}
	r := newTestRunnerForPlan(plan, nodes)
	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached AwaitingReview, got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}

	start := make(chan struct{})
	errCh := make(chan error, 2)
	go func() {
		<-start
		errCh <- r.AcceptPolishedPlan(context.Background(), &CoarsePlan{Request: "accepted"})
	}()
	go func() {
		<-start
		errCh <- r.RejectPolishedPlan("rejected")
	}()
	close(start)

	err1 := <-errCh
	err2 := <-errCh
	successes := 0
	for _, err := range []error{err1, err2} {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrInvalidPhase) {
			t.Fatalf("concurrent transition err = %v, want nil or ErrInvalidPhase", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful transitions = %d, want 1", successes)
	}
	if r.Phase() == PhaseAwaitingReplan {
		close(release)
		return
	}
	close(release)
	waitForRunnerEvent(t, r, EventEngineIdle, "")
	if err := r.Complete(context.Background()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func TestRunner_ReportIncludesDynamicNodes(t *testing.T) {
	plan := &CoarsePlan{Request: "demo"}
	dynamic := &Node{
		Id: "B",
		Agent: &testAgent{
			run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
				return "exec-B", nil, nil
			},
			polish: func(ctx context.Context) (any, error) { return "frag-B", nil },
			report: func(ctx context.Context, output any) (any, error) {
				return "sum-" + output.(string), nil
			},
		},
	}
	nodes := map[string]*Node{
		"A": {
			Id: "A",
			Agent: &testAgent{
				run: func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
					return "exec-A", []*Node{dynamic}, nil
				},
				polish: func(ctx context.Context) (any, error) { return "frag-A", nil },
				report: func(ctx context.Context, output any) (any, error) {
					return "sum-" + output.(string), nil
				},
			},
		},
	}
	r := newTestRunnerForPlan(plan, nodes)
	if err := r.StartPolish(context.Background()); err != nil {
		t.Fatalf("StartPolish: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.Phase() != PhaseAwaitingReview {
		if time.Now().After(deadline) {
			t.Fatalf("phase never reached AwaitingReview, got %q", r.Phase())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.AcceptPolishedPlan(context.Background(), &CoarsePlan{Request: "accepted"}); err != nil {
		t.Fatalf("AcceptPolishedPlan: %v", err)
	}
	waitForRunnerEvent(t, r, EventEngineIdle, "")
	if err := r.Complete(context.Background()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	summaries := r.ReportSummaries()
	if summaries["A"] != "sum-exec-A" {
		t.Fatalf("summary A = %v, want sum-exec-A", summaries["A"])
	}
	if summaries["B"] != "sum-exec-B" {
		t.Fatalf("summary B = %v, want sum-exec-B", summaries["B"])
	}
}

func TestRunner_CompleteRejectedFromWrongPhase(t *testing.T) {
	r := newTestRunner()
	if err := r.Complete(context.Background()); !errors.Is(err, ErrInvalidPhase) {
		t.Fatalf("Complete from PhaseCreated err = %v, want ErrInvalidPhase", err)
	}
}

func TestRunner_AcceptRejectFromWrongPhase(t *testing.T) {
	r := newTestRunner()
	if err := r.AcceptPolishedPlan(context.Background(), &CoarsePlan{}); !errors.Is(err, ErrInvalidPhase) {
		t.Fatalf("Accept from PhaseCreated err = %v, want ErrInvalidPhase", err)
	}
	if err := r.RejectPolishedPlan("x"); !errors.Is(err, ErrInvalidPhase) {
		t.Fatalf("Reject from PhaseCreated err = %v, want ErrInvalidPhase", err)
	}
	if err := r.ReplanWith(context.Background(), &CoarsePlan{}, nil); !errors.Is(err, ErrInvalidPhase) {
		t.Fatalf("ReplanWith from PhaseCreated err = %v, want ErrInvalidPhase", err)
	}
}
