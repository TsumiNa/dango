package orchestrate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQueuedRunnerCanReachAwaitingReviewWithoutConsumingExecutionSlot(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetMaxRunningRunners(1); err != nil {
		t.Fatalf("SetMaxRunningRunners: %v", err)
	}

	firstPlan, firstRunner := mustPlanSingleNodeRunner(t, o)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	mustNodeExecutor(t, firstRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(firstStarted)
		<-firstRelease
		return "first", nil, nil
	}
	if err := o.StartRunner(context.Background(), firstPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(first): %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(firstPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(first): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	secondPlan, _, err := o.planFromRequest(context.Background(), &Request{Input: "run second node"})
	if err != nil {
		t.Fatalf("planFromRequest(second): %v", err)
	}
	if err := o.StartRunner(context.Background(), secondPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(second): %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(secondPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(second): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("second runner did not reach awaiting review while no runner was executing")
}

func TestQueuedRunnerPolishFailureDoesNotLeakExecutionSlot(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetMaxRunningRunners(1); err != nil {
		t.Fatalf("SetMaxRunningRunners: %v", err)
	}

	firstPlan, firstRunner := mustPlanSingleNodeRunner(t, o)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	mustNodeExecutor(t, firstRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(firstStarted)
		<-firstRelease
		return "first", nil, nil
	}
	if err := o.StartRunner(context.Background(), firstPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(first): %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(firstPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(first awaiting review): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.AcceptRunnerPlan(context.Background(), firstPlan.RunnerID, firstPlan); err != nil {
		t.Fatalf("AcceptRunnerPlan(first): %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first runner did not start executing")
	}

	secondPlan, _, err := o.planFromRequest(context.Background(), &Request{Input: "run second node"})
	if err != nil {
		t.Fatalf("planFromRequest(second): %v", err)
	}
	secondRunner, err := o.Runner(secondPlan.RunnerID)
	if err != nil {
		t.Fatalf("Runner(second): %v", err)
	}
	secondRunner.Nodes()["only"].Executor = &stubRunnerExecutor{
		polish: func(ctx context.Context) (any, error) {
			return nil, errors.New("polish failed")
		},
	}
	if err := o.StartRunner(context.Background(), secondPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(second): %v", err)
	}

	close(firstRelease)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(secondPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(second failed): %v", err)
		}
		if view.State.Status == RunnerStatusFailed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(o.runningRunnerIDs); got != 0 {
		t.Fatalf("runningRunnerIDs size after queued polish failure = %d, want 0", got)
	}

	thirdPlan, _, err := o.planFromRequest(context.Background(), &Request{Input: "run third node"})
	if err != nil {
		t.Fatalf("planFromRequest(third): %v", err)
	}
	if err := o.StartRunner(context.Background(), thirdPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(third): %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(thirdPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(third awaiting review): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("third runner did not reach awaiting review after queued polish failure")
}

func TestQueuedDispatchToAwaitingReviewDoesNotConsumeExecutionSlot(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetMaxRunningRunners(1); err != nil {
		t.Fatalf("SetMaxRunningRunners: %v", err)
	}

	firstPlan, firstRunner := mustPlanSingleNodeRunner(t, o)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	mustNodeExecutor(t, firstRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(firstStarted)
		<-firstRelease
		return "first", nil, nil
	}
	if err := o.StartRunner(context.Background(), firstPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(first): %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(firstPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(first awaiting review): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.AcceptRunnerPlan(context.Background(), firstPlan.RunnerID, firstPlan); err != nil {
		t.Fatalf("AcceptRunnerPlan(first): %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first runner did not start executing")
	}

	secondPlan, reject, err := o.StartRequest(context.Background(), &Request{Input: "run second node"})
	if err != nil {
		t.Fatalf("StartRequest(second): %v", err)
	}
	if reject != nil {
		t.Fatalf("reject(second) = %+v, want nil", reject)
	}

	close(firstRelease)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(secondPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(second awaiting review): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(o.runningRunnerIDs); got != 0 {
		t.Fatalf("runningRunnerIDs size after queued runner reached awaiting review = %d, want 0", got)
	}

	thirdPlan, reject, err := o.StartRequest(context.Background(), &Request{Input: "run third node"})
	if err != nil {
		t.Fatalf("StartRequest(third): %v", err)
	}
	if reject != nil {
		t.Fatalf("reject(third) = %+v, want nil", reject)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(thirdPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(third awaiting review): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("third runner did not reach awaiting review after queued dispatch")
}

func TestAcceptRunnerPlan_RespectsExecutionSlotLimit(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetMaxRunningRunners(1); err != nil {
		t.Fatalf("SetMaxRunningRunners: %v", err)
	}
	firstPlan, firstRunner := mustPlanSingleNodeRunner(t, o)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	mustNodeExecutor(t, firstRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(firstStarted)
		<-firstRelease
		return "first", nil, nil
	}
	if err := o.StartRunner(context.Background(), firstPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(first): %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(firstPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(first): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.AcceptRunnerPlan(context.Background(), firstPlan.RunnerID, firstPlan); err != nil {
		t.Fatalf("AcceptRunnerPlan(first): %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first runner did not start executing")
	}
	if len(o.runningRunnerIDs) != 1 {
		t.Fatalf("runningRunnerIDs size = %d, want 1 while first runner is executing", len(o.runningRunnerIDs))
	}

	secondPlan, _, err := o.planFromRequest(context.Background(), &Request{Input: "run second node"})
	if err != nil {
		t.Fatalf("planFromRequest(second): %v", err)
	}
	if err := o.StartRunner(context.Background(), secondPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(second): %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(secondPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(second): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.AcceptRunnerPlan(context.Background(), secondPlan.RunnerID, secondPlan); !errors.Is(err, ErrRunnerExecutionSlotsFull) {
		t.Fatalf("AcceptRunnerPlan(second) err = %v, want ErrRunnerExecutionSlotsFull", err)
	}
	close(firstRelease)
}

func TestStartRequest_QueuesByPriorityWhenLimitReached(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetMaxRunningRunners(1); err != nil {
		t.Fatalf("SetMaxRunningRunners: %v", err)
	}

	firstPlan, firstRunner := mustPlanSingleNodeRunner(t, o)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	mustNodeExecutor(t, firstRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(firstStarted)
		<-firstRelease
		return "first", nil, nil
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	if err := o.StartRunner(firstCtx, firstPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(first): %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(firstPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(first awaiting review): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if view, err := o.QueryRunner(firstPlan.RunnerID); err != nil {
		t.Fatalf("QueryRunner(first before accept): %v", err)
	} else if view.Phase != PhaseAwaitingReview {
		t.Fatalf("first runner phase before accept = %q, want awaiting review", view.Phase)
	}
	if err := o.AcceptRunnerPlan(firstCtx, firstPlan.RunnerID, firstPlan); err != nil {
		t.Fatalf("AcceptRunnerPlan(first): %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first runner to start")
	}

	lowCtx, cancelLow := context.WithCancel(context.Background())
	defer cancelLow()
	lowPlan, reject, err := o.StartRequest(lowCtx, &Request{Input: "low", Priority: 1})
	if err != nil {
		t.Fatalf("StartRequest(low): %v", err)
	}
	if reject != nil {
		t.Fatalf("reject(low) = %+v, want nil", reject)
	}

	highCtx, cancelHigh := context.WithCancel(context.Background())
	defer cancelHigh()
	highPlan, reject, err := o.StartRequest(highCtx, &Request{Input: "high", Priority: RequestPriorityHighest})
	if err != nil {
		t.Fatalf("StartRequest(high): %v", err)
	}
	if reject != nil {
		t.Fatalf("reject(high) = %+v, want nil", reject)
	}

	lowRunner, err := o.Runner(lowPlan.RunnerID)
	if err != nil {
		t.Fatalf("Runner(low): %v", err)
	}
	highRunner, err := o.Runner(highPlan.RunnerID)
	if err != nil {
		t.Fatalf("Runner(high): %v", err)
	}

	lowRelease := make(chan struct{})
	mustNodeExecutor(t, lowRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		<-lowRelease
		return "low", nil, nil
	}
	highStarted := make(chan struct{})
	highRelease := make(chan struct{})
	mustNodeExecutor(t, highRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(highStarted)
		<-highRelease
		return "high", nil, nil
	}
	highAccepted := false

	lowView, err := o.QueryRunner(lowPlan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner(low): %v", err)
	}
	if lowView.State.Status != RunnerStatusPending {
		t.Fatalf("low state = %q, want pending", lowView.State.Status)
	}
	highView, err := o.QueryRunner(highPlan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner(high): %v", err)
	}
	if highView.State.Status != RunnerStatusPending {
		t.Fatalf("high state = %q, want pending", highView.State.Status)
	}

	close(firstRelease)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		highView, err = o.QueryRunner(highPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(high awaiting review): %v", err)
		}
		if highView.Phase == PhaseAwaitingReview {
			if err := o.AcceptRunnerPlan(highCtx, highPlan.RunnerID, highPlan); err != nil {
				t.Fatalf("AcceptRunnerPlan(high): %v", err)
			}
			highAccepted = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !highAccepted {
		t.Fatal("high priority runner did not reach awaiting review")
	}
	select {
	case <-highStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for high priority runner to start")
	}
	time.Sleep(150 * time.Millisecond)
	lowView, err = o.QueryRunner(lowPlan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner(low after high start): %v", err)
	}
	if lowView.State.Status != RunnerStatusPending {
		t.Fatalf("low state after high start = %q, want pending", lowView.State.Status)
	}

	close(highRelease)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		highView, err = o.QueryRunner(highPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(high after release): %v", err)
		}
		lowView, err = o.QueryRunner(lowPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(low after high drain): %v", err)
		}
		if highView.State.Status == RunnerStatusIdle && lowView.Phase == PhaseAwaitingReview {
			if err := o.AcceptRunnerPlan(lowCtx, lowPlan.RunnerID, lowPlan); err != nil {
				t.Fatalf("AcceptRunnerPlan(low): %v", err)
			}
		}
		if lowView.State.Status == RunnerStatusRunning || lowView.State.Status == RunnerStatusIdle {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lowView.State.Status != RunnerStatusRunning && lowView.State.Status != RunnerStatusIdle {
		t.Fatalf("low state after high drain = %q, want running or idle", lowView.State.Status)
	}

	close(lowRelease)
	cancelFirst()
	cancelHigh()
	cancelLow()
}

func TestStartRequest_CanceledWhileQueuedTransitionsRunner(t *testing.T) {
	o := newOrchestrator(testLogger)
	if err := o.SetMaxRunningRunners(1); err != nil {
		t.Fatalf("SetMaxRunningRunners: %v", err)
	}

	firstPlan, firstRunner := mustPlanSingleNodeRunner(t, o)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	mustNodeExecutor(t, firstRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(firstStarted)
		<-firstRelease
		return "first", nil, nil
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	if err := o.StartRunner(firstCtx, firstPlan.RunnerID); err != nil {
		t.Fatalf("StartRunner(first): %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		view, err := o.QueryRunner(firstPlan.RunnerID)
		if err != nil {
			t.Fatalf("QueryRunner(first awaiting review): %v", err)
		}
		if view.Phase == PhaseAwaitingReview {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := o.AcceptRunnerPlan(firstCtx, firstPlan.RunnerID, firstPlan); err != nil {
		t.Fatalf("AcceptRunnerPlan(first): %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first runner to start")
	}

	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	queuedPlan, reject, err := o.StartRequest(queuedCtx, &Request{Input: "queued", Priority: 2})
	if err != nil {
		t.Fatalf("StartRequest(queued): %v", err)
	}
	if reject != nil {
		t.Fatalf("reject(queued) = %+v, want nil", reject)
	}
	queuedRunner, err := o.Runner(queuedPlan.RunnerID)
	if err != nil {
		t.Fatalf("Runner(queued): %v", err)
	}
	queuedStarted := make(chan struct{})
	mustNodeExecutor(t, queuedRunner.Nodes()["only"]).RunE = func(ctx context.Context, parentOutputs map[string]any) (any, []*Node, error) {
		close(queuedStarted)
		return "queued", nil, nil
	}
	updates, unsubscribe, err := o.SubscribeRunner(queuedPlan.RunnerID, 4)
	if err != nil {
		t.Fatalf("SubscribeRunner: %v", err)
	}
	defer unsubscribe()
	waitForRunnerUpdate(t, updates, func(update RunnerUpdate) bool {
		return update.Event == nil && update.State.Status == RunnerStatusPending
	}, "queued initial pending update")

	cancelQueued()
	terminalUpdate := waitForRunnerUpdate(t, updates, func(update RunnerUpdate) bool {
		return update.State.Status == RunnerStatusCanceled
	}, "queued canceled update")
	if terminalUpdate.State.Status != RunnerStatusCanceled {
		t.Fatalf("queued state = %q, want canceled", terminalUpdate.State.Status)
	}
	waitForRunnerUpdateClosed(t, updates, "queued canceled update")

	close(firstRelease)
	select {
	case <-queuedStarted:
		t.Fatal("queued runner started after its submission context was canceled")
	case <-time.After(200 * time.Millisecond):
	}
	view, err := o.QueryRunner(queuedPlan.RunnerID)
	if err != nil {
		t.Fatalf("QueryRunner(queued): %v", err)
	}
	if view.State.Status != RunnerStatusCanceled {
		t.Fatalf("queued final state = %q, want canceled", view.State.Status)
	}
	cancelFirst()
}
