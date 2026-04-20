package orchestrate

import (
	"context"
	"errors"
	"sync"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/orchestrate/runner"
)

// ManagedRunner is the Orchestrator-owned runner record created for a plan.
//
// Runner is the execution engine that will eventually run the graph, Plan is
// the coarse plan that produced it, and Nodes is the materialized node graph
// keyed by node ID for later inspection or startup.
type ManagedRunner struct {
	Runner *runnerpkg.Runner
	Plan   *CoarsePlan
	Nodes  map[string]*runnerpkg.Node

	mu                   sync.RWMutex
	snapshot             runnerpkg.RunnerSnapshot
	started              bool
	stoppedEventSeen     bool
	cancel               context.CancelFunc
	subscribers          map[uint64]chan RunnerUpdate
	nextSubscriberID     uint64
	onExecutionDrained   func()
	executionDrainedOnce sync.Once
}

// RunnerView is the query-facing snapshot Orchestrator exposes for one managed
// runner.
type RunnerView struct {
	RunnerID string                   `json:"runner_id" yaml:"runner_id"`
	Plan     *CoarsePlan              `json:"plan,omitempty" yaml:"plan,omitempty"`
	State    runnerpkg.RunnerState    `json:"state" yaml:"state"`
	Snapshot runnerpkg.RunnerSnapshot `json:"snapshot" yaml:"snapshot"`
}

// RunnerUpdate is the stream-facing update Orchestrator forwards to outer
// callers as a runner changes state.
type RunnerUpdate struct {
	RunnerID string                   `json:"runner_id" yaml:"runner_id"`
	State    runnerpkg.RunnerState    `json:"state" yaml:"state"`
	Snapshot runnerpkg.RunnerSnapshot `json:"snapshot" yaml:"snapshot"`
	Event    *runnerpkg.RunnerEvent   `json:"event,omitempty" yaml:"event,omitempty"`
}

func newManagedRunner(runner *runnerpkg.Runner, plan *CoarsePlan, nodes map[string]*runnerpkg.Node) *ManagedRunner {
	return &ManagedRunner{
		Runner:      runner,
		Plan:        cloneCoarsePlan(plan),
		Nodes:       nodes,
		snapshot:    buildInitialRunnerSnapshot(nodes),
		subscribers: make(map[uint64]chan RunnerUpdate),
	}
}

func (m *ManagedRunner) view() *RunnerView {
	m.mu.RLock()
	snapshot := cloneRunnerSnapshot(m.snapshot)
	plan := cloneCoarsePlan(m.Plan)
	m.mu.RUnlock()
	return &RunnerView{
		RunnerID: m.Runner.ID(),
		Plan:     plan,
		State:    m.Runner.State(),
		Snapshot: snapshot,
	}
}

func (m *ManagedRunner) subscribe(bufferSize int) (<-chan RunnerUpdate, func()) {
	if bufferSize < 1 {
		bufferSize = 1
	}
	ch := make(chan RunnerUpdate, bufferSize)

	m.mu.Lock()
	update := RunnerUpdate{
		RunnerID: m.Runner.ID(),
		State:    m.Runner.State(),
		Snapshot: cloneRunnerSnapshot(m.snapshot),
	}
	terminal := runnerTerminal(update.State)
	if terminal {
		ch <- update
		close(ch)
		m.mu.Unlock()
		return ch, func() {}
	}
	id := m.nextSubscriberID
	m.nextSubscriberID++
	m.subscribers[id] = ch
	ch <- update
	m.mu.Unlock()

	unsubscribe := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if sub := m.subscribers[id]; sub != nil {
			delete(m.subscribers, id)
			close(sub)
		}
	}
	return ch, unsubscribe
}

func (m *ManagedRunner) start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return runnerpkg.ErrRunnerAlreadyStarted
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.started = true
	m.cancel = cancel
	rootNodes := make([]*runnerpkg.Node, 0, len(m.Nodes))
	for _, node := range m.Nodes {
		rootNodes = append(rootNodes, node)
	}
	events := m.Runner.Subscribe(64)
	m.mu.Unlock()

	if err := m.Runner.AddNodes(runCtx, rootNodes...); err != nil {
		cancel()
		m.mu.Lock()
		m.started = false
		m.cancel = nil
		m.mu.Unlock()
		return err
	}

	go m.forwardRunnerEvents(events)
	go m.runRunner(runCtx)
	return nil
}

func (m *ManagedRunner) runRunner(ctx context.Context) {
	err := m.Runner.Start(ctx)
	m.mu.Lock()
	stoppedSeen := m.stoppedEventSeen
	m.cancel = nil
	m.mu.Unlock()
	if !stoppedSeen {
		m.publishUpdate(nil)
	}
	if runnerTerminal(m.Runner.State()) {
		m.closeSubscribers()
	}
	_ = err
}

func (m *ManagedRunner) forwardRunnerEvents(events <-chan runnerpkg.RunnerEvent) {
	for event := range events {
		ev := event
		m.publishUpdate(&ev)
		if ev.Type == runnerpkg.EventEngineStopped {
			return
		}
	}
}

func (m *ManagedRunner) publishUpdate(event *runnerpkg.RunnerEvent) {
	state := m.Runner.State()
	snapshot := m.currentSnapshot(event)
	shouldDrain := state.Status == runnerpkg.RunnerStatusIdle || runnerTerminal(state)
	update := RunnerUpdate{
		RunnerID: m.Runner.ID(),
		State:    state,
		Snapshot: snapshot,
		Event:    event,
	}

	m.mu.Lock()
	m.snapshot = cloneRunnerSnapshot(snapshot)
	if event != nil && event.Type == runnerpkg.EventEngineStopped {
		m.stoppedEventSeen = true
	}
	for _, ch := range m.subscribers {
		select {
		case ch <- update:
		default:
		}
	}
	m.mu.Unlock()
	if shouldDrain {
		m.executionDrainedOnce.Do(func() {
			if m.onExecutionDrained != nil {
				m.onExecutionDrained()
			}
		})
	}
}

func (m *ManagedRunner) finishBeforeStart(runErr error) {
	m.Runner.FinishBeforeStart(runErr)
	m.publishUpdate(nil)
	m.closeSubscribers()
}

func (m *ManagedRunner) currentSnapshot(event *runnerpkg.RunnerEvent) runnerpkg.RunnerSnapshot {
	if event == nil || event.Type == runnerpkg.EventEngineStopped {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return cloneRunnerSnapshot(m.snapshot)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	snapshot, err := m.Runner.GetSnapshot(ctx)
	if err != nil {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return cloneRunnerSnapshot(m.snapshot)
	}
	return snapshot
}

func (m *ManagedRunner) closeSubscribers() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, ch := range m.subscribers {
		close(ch)
		delete(m.subscribers, id)
	}
}

func cloneCoarsePlan(plan *CoarsePlan) *CoarsePlan {
	if plan == nil {
		return nil
	}
	copyPlan := *plan
	copyPlan.Nodes = make([]CoarsePlanNode, len(plan.Nodes))
	for i, node := range plan.Nodes {
		copyPlan.Nodes[i] = node
		copyPlan.Nodes[i].DependsOn = append([]string(nil), node.DependsOn...)
	}
	return &copyPlan
}

func buildInitialRunnerSnapshot(nodes map[string]*runnerpkg.Node) runnerpkg.RunnerSnapshot {
	snapshot := runnerpkg.RunnerSnapshot{
		CompletedNodes: make(map[string]any),
		PendingNodes:   make(map[string]int, len(nodes)),
		GraphEdges:     make(map[string][]string),
		NodesData:      make(map[string]*runnerpkg.Node, len(nodes)),
	}
	for id, node := range nodes {
		snapshot.NodesData[id] = node
		snapshot.PendingNodes[id] = len(node.Parents)
		for _, parent := range node.Parents {
			snapshot.GraphEdges[parent.Id] = append(snapshot.GraphEdges[parent.Id], id)
		}
	}
	return snapshot
}

func cloneRunnerSnapshot(snapshot runnerpkg.RunnerSnapshot) runnerpkg.RunnerSnapshot {
	copySnapshot := runnerpkg.RunnerSnapshot{
		ActiveCount:    snapshot.ActiveCount,
		CompletedNodes: make(map[string]any, len(snapshot.CompletedNodes)),
		PendingNodes:   make(map[string]int, len(snapshot.PendingNodes)),
		GraphEdges:     make(map[string][]string, len(snapshot.GraphEdges)),
		NodesData:      make(map[string]*runnerpkg.Node, len(snapshot.NodesData)),
	}
	for id, output := range snapshot.CompletedNodes {
		copySnapshot.CompletedNodes[id] = output
	}
	for id, pending := range snapshot.PendingNodes {
		copySnapshot.PendingNodes[id] = pending
	}
	for id, children := range snapshot.GraphEdges {
		copySnapshot.GraphEdges[id] = append([]string(nil), children...)
	}
	for id, node := range snapshot.NodesData {
		copySnapshot.NodesData[id] = node
	}
	return copySnapshot
}

func runnerRemovable(state runnerpkg.RunnerState) bool {
	switch state.Status {
	case runnerpkg.RunnerStatusPending, runnerpkg.RunnerStatusFailed, runnerpkg.RunnerStatusCanceled:
		return true
	default:
		return false
	}
}

func runnerTerminal(state runnerpkg.RunnerState) bool {
	switch state.Status {
	case runnerpkg.RunnerStatusFailed, runnerpkg.RunnerStatusCanceled:
		return true
	default:
		return false
	}
}

func runnerStatusFromStartError(runErr error) runnerpkg.RunnerStatus {
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return runnerpkg.RunnerStatusCanceled
	}
	return runnerpkg.RunnerStatusFailed
}
