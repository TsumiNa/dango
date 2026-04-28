package runner

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lithammer/shortuuid/v4"
	"github.com/tsumina/dango/internal/llm"
)

// Runner is the execution engine that drives a [CoarsePlan] through its
// phased lifecycle and exposes both engine-level and managed observation
// surfaces.
//
// Runner absorbs the historical ManagedRunner role: it owns the plan, the
// materialized node graph, a subscriber set for high-level updates, a Done
// signal for settle-detection, and the engine event loop that dispatches
// nodes by dependency readiness. Callers construct a Runner with [New] and
// any number of [Option]s.
type Runner struct {
	id     string
	logger *slog.Logger

	// Startup configuration set once via Options.
	store             RunnerStore
	plan              *CoarsePlan
	initialNodes      map[string]*Node
	skillSessionStore llm.SessionStore
	skillSessionIDs   map[string]string
	skillSessionMu    sync.Mutex

	// Engine-level lifecycle state.
	stateMu sync.RWMutex
	state   RunnerState
	phase   RunnerPhase

	// Lifecycle control.
	startOnce    sync.Once
	startErr     error
	settleOnce   sync.Once
	done         chan struct{}
	doneOnce     sync.Once
	engineErr    error
	engineErrMu  sync.RWMutex
	cancelEngine context.CancelFunc

	// completedCleanly is flipped to true by [Runner.Complete] to signal
	// that a subsequent engine cancel should be treated as graceful
	// completion (success path, triggers [PhaseReport]).
	completedCleanly atomic.Bool

	// Engine event-loop channels.
	addNodeCh chan []*Node
	resultCh  chan executionResult
	queryCh   chan chan<- RunnerSnapshot

	// Low-level event subscribers (RunnerEvent stream).
	subMutex    sync.RWMutex
	subscribers []chan<- RunnerEvent

	// High-level managed update subscribers (RunnerUpdate stream).
	updateMu          sync.Mutex
	snapshot          RunnerSnapshot
	stoppedEventSeen  bool
	nextSubscriberID  uint64
	updateSubscribers map[uint64]chan RunnerUpdate

	// Phased-lifecycle bookkeeping: results gathered by the polish and
	// report stages, and the rejection reason captured when a polished
	// plan is rejected.
	lifecycleMu     sync.RWMutex
	polishFragments map[string]any
	polishErr       error
	reportSummaries map[string]any
	reportErr       error
	replanReason    string
}

// New constructs a Runner configured by the provided options.
//
// Options configure logger, persistence store, and an optional [CoarsePlan]
// with its materialized node graph. Without [WithPlan], the Runner operates
// as a bare execution engine: callers drive it by invoking [Runner.AddNodes]
// after [Runner.Start].
func New(opts ...Option) *Runner {
	r := &Runner{
		id:                shortuuid.New(),
		logger:            slog.Default(),
		state:             RunnerState{Status: RunnerStatusPending},
		phase:             PhaseCreated,
		done:              make(chan struct{}),
		addNodeCh:         make(chan []*Node, 1),
		resultCh:          make(chan executionResult),
		queryCh:           make(chan chan<- RunnerSnapshot),
		subscribers:       make([]chan<- RunnerEvent, 0),
		updateSubscribers: make(map[uint64]chan RunnerUpdate),
		skillSessionStore: newMemorySessionStore(),
		skillSessionIDs:   make(map[string]string),
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.plan != nil && r.plan.RunnerID == "" {
		r.plan.RunnerID = r.id
	}
	r.snapshot = buildInitialRunnerSnapshot(r.initialNodes)
	return r
}

// ID returns the stable identifier assigned to this Runner at creation time.
func (r *Runner) ID() string { return r.id }

// State returns the current engine-level lifecycle snapshot.
func (r *Runner) State() RunnerState {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.state
}

// Phase returns the current high-level plan phase.
func (r *Runner) Phase() RunnerPhase {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.phase
}

// Plan returns the [CoarsePlan] the runner was constructed with via
// [WithPlan], or nil in bare mode.
func (r *Runner) Plan() *CoarsePlan {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return CloneCoarsePlan(r.plan)
}

// Nodes returns the initial node graph supplied via [WithPlan], keyed by
// node ID, or nil in bare mode.
func (r *Runner) Nodes() map[string]*Node {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return cloneNodeMap(r.initialNodes)
}

func cloneNodeMap(nodes map[string]*Node) map[string]*Node {
	if nodes == nil {
		return nil
	}
	copyNodes := make(map[string]*Node, len(nodes))
	for id, node := range nodes {
		copyNodes[id] = node
	}
	return copyNodes
}

func nodeMapSlice(nodes map[string]*Node) []*Node {
	if len(nodes) == 0 {
		return nil
	}
	list := make([]*Node, 0, len(nodes))
	for _, node := range nodes {
		list = append(list, node)
	}
	return list
}

// Done returns a channel that closes when the runner has settled into a
// terminal phase ([PhaseSettled]).
func (r *Runner) Done() <-chan struct{} { return r.done }

// Wait blocks until the runner settles or ctx is canceled, returning the
// final engine error (if any) or ctx.Err on timeout.
//
// If the runner is already settled when Wait is called (or settles while
// ctx is simultaneously canceled), Wait returns the engine error rather
// than ctx.Err.
func (r *Runner) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-r.done:
		r.engineErrMu.RLock()
		defer r.engineErrMu.RUnlock()
		return r.engineErr
	default:
	}
	select {
	case <-r.done:
		r.engineErrMu.RLock()
		defer r.engineErrMu.RUnlock()
		return r.engineErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Subscribe returns a channel that receives low-level [RunnerEvent]s from
// the engine event loop. The returned channel remains open for the lifetime
// of the runner; callers are responsible for draining or discarding.
func (r *Runner) Subscribe(bufferSize int) <-chan RunnerEvent {
	ch := make(chan RunnerEvent, bufferSize)
	r.subMutex.Lock()
	defer r.subMutex.Unlock()
	r.subscribers = append(r.subscribers, ch)
	return ch
}

// SubscribeUpdates returns a channel that receives high-level
// [RunnerUpdate]s and an unsubscribe function.
//
// The first delivery is the current snapshot. If the runner is already in a
// terminal phase at subscription time, the snapshot is delivered and the
// channel is closed immediately.
func (r *Runner) SubscribeUpdates(bufferSize int) (<-chan RunnerUpdate, func()) {
	if bufferSize < 1 {
		bufferSize = 1
	}
	ch := make(chan RunnerUpdate, bufferSize)
	state := r.State()
	phase := r.Phase()

	r.updateMu.Lock()
	update := RunnerUpdate{
		RunnerID: r.id,
		State:    state,
		Phase:    phase,
		Snapshot: cloneRunnerSnapshot(r.snapshot),
	}
	if IsTerminal(state) {
		ch <- update
		close(ch)
		r.updateMu.Unlock()
		return ch, func() {}
	}
	id := r.nextSubscriberID
	r.nextSubscriberID++
	r.updateSubscribers[id] = ch
	ch <- update
	r.updateMu.Unlock()

	unsubscribe := func() {
		r.updateMu.Lock()
		defer r.updateMu.Unlock()
		if sub := r.updateSubscribers[id]; sub != nil {
			delete(r.updateSubscribers, id)
			close(sub)
		}
	}
	return ch, unsubscribe
}

// View returns a point-in-time snapshot of the runner suitable for query
// APIs.
func (r *Runner) View() *RunnerView {
	r.updateMu.Lock()
	snapshot := cloneRunnerSnapshot(r.snapshot)
	r.updateMu.Unlock()
	return &RunnerView{
		RunnerID: r.id,
		Plan:     r.Plan(),
		State:    r.State(),
		Phase:    r.Phase(),
		Snapshot: snapshot,
	}
}

// AddNodes queues new nodes to be added to the execution graph. It blocks
// until the event loop accepts the nodes or ctx is canceled.
func (r *Runner) AddNodes(ctx context.Context, nodes ...*Node) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case r.addNodeCh <- nodes:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetSnapshot safely queries the current graph and execution state.
func (r *Runner) GetSnapshot(ctx context.Context) (RunnerSnapshot, error) {
	replyCh := make(chan RunnerSnapshot, 1)
	select {
	case r.queryCh <- replyCh:
		return <-replyCh, nil
	case <-ctx.Done():
		return RunnerSnapshot{}, ctx.Err()
	}
}

// Start launches the runner's execution engine in the background.
//
// Start is the bare entry point: it transitions directly from [PhaseCreated]
// into [PhaseExecuting], skipping [PhasePolishing] and [PhaseAwaitingReview].
// Callers wiring the full phased lifecycle should use [Runner.StartPolish]
// followed by [Runner.AcceptPolishedPlan]. When [WithPlan] supplied initial
// nodes they are added before the engine event loop begins. The returned
// error is non-nil only if the runner is not in [PhaseCreated], if it has
// already been started, or if queueing initial nodes fails; the engine's
// ultimate error is retrieved via [Runner.Wait] after the runner settles.
func (r *Runner) Start(ctx context.Context) error {
	initialNodes, err := r.prepareEngineLaunch(PhaseCreated, nil)
	if err != nil {
		return err
	}
	return r.launchEngine(ctx, initialNodes)
}

func (r *Runner) prepareEngineLaunch(from RunnerPhase, mutate func()) ([]*Node, error) {
	if err := r.prepareNodeExecutors(r.initialNodes); err != nil {
		return nil, err
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.state.Status != RunnerStatusPending {
		return nil, ErrRunnerAlreadyStarted
	}
	if r.phase != from {
		return nil, ErrInvalidPhase
	}
	if mutate != nil {
		mutate()
	}
	r.phase = PhaseExecuting
	return nodeMapSlice(r.initialNodes), nil
}

// launchEngine spawns the engine goroutine once after the caller has
// already reserved the transition into [PhaseExecuting].
func (r *Runner) launchEngine(ctx context.Context, initialNodes []*Node) error {
	if ctx == nil {
		ctx = context.Background()
	}

	started := false
	r.startOnce.Do(func() {
		started = true

		runCtx, cancel := context.WithCancel(ctx)
		r.cancelEngine = cancel

		events := r.Subscribe(64)

		go r.forwardEngineEvents(events)
		go func() {
			err := r.runEngine(runCtx)
			r.settle(err)
		}()

		if len(initialNodes) > 0 {
			if err := r.AddNodes(runCtx, initialNodes...); err != nil {
				r.startErr = err
				cancel()
				return
			}
		}
	})

	if !started {
		return ErrRunnerAlreadyStarted
	}
	return r.startErr
}

// Abort marks the runner terminal without running the engine, publishing a
// final update and closing managed subscribers. It replaces the historical
// FinishBeforeStart entry point.
func (r *Runner) Abort(runErr error) {
	status := RunnerStatusFailed
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		status = RunnerStatusCanceled
	}
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	now := time.Now()

	r.stateMu.Lock()
	r.state = RunnerState{
		Status:     status,
		UpdatedAt:  now,
		FinishedAt: now,
		Error:      errText,
	}
	r.phase = PhaseSettled
	r.stateMu.Unlock()

	r.captureEngineErr(runErr)
	r.publishTerminalUpdate(nil)
	r.markDone()
}

func (r *Runner) transitionPhase(phase RunnerPhase) {
	r.stateMu.Lock()
	r.phase = phase
	r.stateMu.Unlock()
}

// settle is the single terminal path for the engine goroutine. It runs the
// [PhaseReport] stage when the engine stopped as a result of
// [Runner.Complete] and otherwise propagates the engine error. It is
// idempotent via [sync.Once].
func (r *Runner) settle(engineErr error) {
	r.settleOnce.Do(func() {
		clean := r.completedCleanly.Load()
		runErr := engineErr
		if clean && errors.Is(runErr, context.Canceled) {
			runErr = nil
			now := time.Now()
			r.stateMu.Lock()
			r.state = RunnerState{
				Status:     RunnerStatusIdle,
				UpdatedAt:  now,
				FinishedAt: now,
			}
			r.stateMu.Unlock()
		}
		r.captureEngineErr(runErr)
		hasPlan := r.Plan() != nil
		if runErr == nil && hasPlan {
			r.runReportStage(context.Background())
		}
		r.transitionPhase(PhaseSettled)
		r.publishTerminalUpdate(nil)
		r.markDone()
	})
}

func (r *Runner) markDone() {
	r.doneOnce.Do(func() { close(r.done) })
}

func (r *Runner) captureEngineErr(err error) {
	if err == nil {
		return
	}
	r.engineErrMu.Lock()
	if r.engineErr == nil {
		r.engineErr = err
	}
	r.engineErrMu.Unlock()
}

func (r *Runner) forwardEngineEvents(events <-chan RunnerEvent) {
	for ev := range events {
		event := ev
		r.publishUpdate(&event)
		if event.Type == EventEngineStopped {
			return
		}
	}
}

func (r *Runner) publishUpdate(event *RunnerEvent) {
	state := r.State()
	phase := r.Phase()
	snapshot := r.currentSnapshot(event)
	update := RunnerUpdate{
		RunnerID: r.id,
		State:    state,
		Phase:    phase,
		Snapshot: snapshot,
		Event:    event,
	}

	r.updateMu.Lock()
	r.snapshot = cloneRunnerSnapshot(snapshot)
	if event != nil && event.Type == EventEngineStopped {
		r.stoppedEventSeen = true
	}
	for _, ch := range r.updateSubscribers {
		select {
		case ch <- update:
		default:
		}
	}
	r.updateMu.Unlock()
}

func (r *Runner) publishTerminalUpdate(event *RunnerEvent) {
	state := r.State()
	phase := r.Phase()
	snapshot := r.currentSnapshot(event)
	update := RunnerUpdate{
		RunnerID: r.id,
		State:    state,
		Phase:    phase,
		Snapshot: snapshot,
		Event:    event,
	}

	r.updateMu.Lock()
	r.snapshot = cloneRunnerSnapshot(snapshot)
	if event != nil && event.Type == EventEngineStopped {
		r.stoppedEventSeen = true
	}
	for id, ch := range r.updateSubscribers {
		select {
		case ch <- update:
		default:
		}
		close(ch)
		delete(r.updateSubscribers, id)
	}
	r.updateMu.Unlock()
}

func (r *Runner) currentSnapshot(event *RunnerEvent) RunnerSnapshot {
	if event == nil || event.Type == EventEngineStopped {
		r.updateMu.Lock()
		defer r.updateMu.Unlock()
		return cloneRunnerSnapshot(r.snapshot)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	snapshot, err := r.GetSnapshot(ctx)
	if err != nil {
		r.updateMu.Lock()
		defer r.updateMu.Unlock()
		return cloneRunnerSnapshot(r.snapshot)
	}
	return snapshot
}

func (r *Runner) closeUpdateSubscribers() {
	r.updateMu.Lock()
	defer r.updateMu.Unlock()
	for id, ch := range r.updateSubscribers {
		close(ch)
		delete(r.updateSubscribers, id)
	}
}

// runEngine is the blocking event loop that drives node execution. It is
// invoked once by [Runner.Start] in a background goroutine.
func (r *Runner) runEngine(ctx context.Context) error {
	r.stateMu.Lock()
	if r.state.Status != RunnerStatusPending {
		r.stateMu.Unlock()
		return ErrRunnerAlreadyStarted
	}
	store := r.store
	now := time.Now()
	r.state = RunnerState{
		Status:    RunnerStatusRunning,
		StartedAt: now,
		UpdatedAt: now,
	}
	initialState := r.state
	r.stateMu.Unlock()

	if err := r.appendRecord(store, &RunnerRecord{Kind: RunnerRecordInit}); err != nil {
		_, _ = r.transitionState(RunnerStatusFailed, err, true)
		return err
	}
	if err := r.appendRecord(store, &RunnerRecord{Kind: RunnerRecordStatus, Status: initialState.Status}); err != nil {
		_, _ = r.transitionState(RunnerStatusFailed, err, true)
		return err
	}

	r.logger.Info("Starting execution engine event loop...")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	outputs := make(map[string]any)
	pendingParents := make(map[string]int)
	nodes := make(map[string]*Node)
	children := make(map[string][]*Node)
	activeCount := 0

	broadcast := func(event RunnerEvent) {
		r.subMutex.RLock()
		defer r.subMutex.RUnlock()
		for _, ch := range r.subscribers {
			select {
			case ch <- event:
			default:
			}
		}
	}

	emitEvent := func(event RunnerEvent) error {
		if err := r.appendRecord(store, &RunnerRecord{Kind: RunnerRecordEvent, Event: newStoredRunnerEvent(event)}); err != nil {
			return err
		}
		broadcast(event)
		return nil
	}

	buildRuntimeSnapshot := func() RunnerSnapshot {
		snapshot := RunnerSnapshot{
			CompletedNodes: make(map[string]any, len(outputs)),
			PendingNodes:   make(map[string]int, len(pendingParents)),
			GraphEdges:     make(map[string][]string, len(children)),
			NodesData:      make(map[string]*Node, len(nodes)),
			ActiveCount:    activeCount,
		}
		for id, output := range outputs {
			snapshot.CompletedNodes[id] = output
		}
		for id, node := range nodes {
			snapshot.NodesData[id] = node
		}
		for id, pending := range pendingParents {
			snapshot.PendingNodes[id] = pending
		}
		for parentID, childNodes := range children {
			childIDs := make([]string, 0, len(childNodes))
			for _, child := range childNodes {
				childIDs = append(childIDs, child.Id)
			}
			snapshot.GraphEdges[parentID] = childIDs
		}
		return snapshot
	}

	finish := func(status RunnerStatus, runErr error) error {
		// Snapshot the final engine state onto the runner so the settle path
		// (including the Report stage) can see dynamic nodes and outputs
		// after the event loop has exited.
		snapshot := buildRuntimeSnapshot()
		r.updateMu.Lock()
		r.snapshot = snapshot
		r.updateMu.Unlock()

		statusErr := r.recordState(store, status, runErr, true)
		stopErr := emitEvent(RunnerEvent{Type: EventEngineStopped})
		return errors.Join(runErr, statusErr, stopErr)
	}

	runNode := func(n *Node) error {
		inputs := make(map[string]any)
		for _, p := range n.Parents {
			inputs[p.Id] = outputs[p.Id]
		}

		n.UpdatedAt = time.Now()
		activeCount++
		if err := r.recordState(store, RunnerStatusRunning, nil, false); err != nil {
			activeCount--
			return err
		}
		if err := emitEvent(RunnerEvent{Type: EventNodeStarted, NodeID: n.Id}); err != nil {
			activeCount--
			return err
		}

		go func() {
			out, dynNodes, err := n.Executor.Execute(ctx, inputs)
			select {
			case <-ctx.Done():
			case r.resultCh <- executionResult{nodeID: n.Id, output: out, newNodes: dynNodes, err: err}:
			}
		}()
		return nil
	}

	addNodesInternal := func(newNodes []*Node) error {
		now := time.Now()
		for _, n := range newNodes {
			if _, exists := nodes[n.Id]; exists {
				continue
			}

			if n.CreatedAt.IsZero() {
				n.CreatedAt = now
			}
			n.UpdatedAt = now
			nodes[n.Id] = n

			for _, p := range n.Parents {
				children[p.Id] = append(children[p.Id], n)
			}

			pending := 0
			for _, p := range n.Parents {
				if _, done := outputs[p.Id]; !done {
					pending++
				}
			}
			pendingParents[n.Id] = pending

			if err := emitEvent(RunnerEvent{Type: EventNodeAdded, NodeID: n.Id}); err != nil {
				return err
			}

			if pending == 0 {
				if err := runNode(n); err != nil {
					return err
				}
			}
		}
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return finish(RunnerStatusCanceled, ctx.Err())

		case newNodes := <-r.addNodeCh:
			if err := addNodesInternal(newNodes); err != nil {
				return finish(RunnerStatusFailed, err)
			}

		case res := <-r.resultCh:
			n := nodes[res.nodeID]
			n.UpdatedAt = time.Now()
			n.FinishedAt = n.UpdatedAt
			activeCount--

			if res.err != nil {
				r.logger.Error("Node execution failed, terminating chain.", "node_id", res.nodeID, "error", res.err)
				eventErr := emitEvent(RunnerEvent{Type: EventNodeFailed, NodeID: res.nodeID, Data: res.err})
				return finish(RunnerStatusFailed, errors.Join(res.err, eventErr))
			}

			if err := addNodesInternal(res.newNodes); err != nil {
				return finish(RunnerStatusFailed, err)
			}

			outputs[res.nodeID] = res.output
			if err := emitEvent(RunnerEvent{Type: EventNodeCompleted, NodeID: res.nodeID, Data: res.output}); err != nil {
				return finish(RunnerStatusFailed, err)
			}

			for _, child := range children[res.nodeID] {
				pendingParents[child.Id]--
				if pendingParents[child.Id] == 0 {
					if err := runNode(child); err != nil {
						return finish(RunnerStatusFailed, err)
					}
				}
			}
			if activeCount == 0 {
				if err := r.recordState(store, RunnerStatusIdle, nil, false); err != nil {
					return finish(RunnerStatusFailed, err)
				}
				if err := emitEvent(RunnerEvent{Type: EventEngineIdle}); err != nil {
					return finish(RunnerStatusFailed, err)
				}
			}

		case replyCh := <-r.queryCh:
			replyCh <- buildRuntimeSnapshot()
		}
	}
}
