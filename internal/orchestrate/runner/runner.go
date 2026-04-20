package runner

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/lithammer/shortuuid/v4"
)

// Runner acts as an execution engine that schedules and runs Nodes based on dependencies.
// It manages state strictly inside a single event loop to safely support concurrency.
type Runner struct {
	id     string
	logger *slog.Logger

	stateMu sync.RWMutex
	state   RunnerState
	store   RunnerStore

	// Core channels for the event loop.
	addNodeCh chan []*Node
	resultCh  chan executionResult
	queryCh   chan chan<- RunnerSnapshot

	// Subscription mechanisms.
	subscribers []chan<- RunnerEvent
	subMutex    sync.RWMutex
}

// ID returns the stable identifier assigned to this Runner at creation time.
func (r *Runner) ID() string { return r.id }

// State returns the current lifecycle snapshot of the Runner.
func (r *Runner) State() RunnerState {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.state
}

// SetStore configures an append-only persistence store for the Runner.
//
// It may only be called before [Runner.Start]. Passing nil clears any
// previously configured store.
func (r *Runner) SetStore(store RunnerStore) error {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.state.Status != RunnerStatusPending {
		return ErrRunnerAlreadyStarted
	}
	r.store = store
	return nil
}

// NewRunner initializes a new Runner instance.
func NewRunner(logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{
		id:          shortuuid.New(),
		logger:      logger,
		state:       RunnerState{Status: RunnerStatusPending},
		addNodeCh:   make(chan []*Node, 1),
		resultCh:    make(chan executionResult),
		queryCh:     make(chan chan<- RunnerSnapshot),
		subscribers: make([]chan<- RunnerEvent, 0),
	}
}

// Subscribe returns a channel to receive runner lifecycle events dynamically.
// It acts as a publish-subscribe mechanism for upper layers.
func (r *Runner) Subscribe(bufferSize int) <-chan RunnerEvent {
	ch := make(chan RunnerEvent, bufferSize)
	r.subMutex.Lock()
	defer r.subMutex.Unlock()
	r.subscribers = append(r.subscribers, ch)
	return ch
}

// GetSnapshot safely queries the current graph and execution state.
// It blocks until the event loop replies or the given context cancels.
func (r *Runner) GetSnapshot(ctx context.Context) (RunnerSnapshot, error) {
	replyCh := make(chan RunnerSnapshot, 1)
	select {
	case r.queryCh <- replyCh:
		return <-replyCh, nil
	case <-ctx.Done():
		return RunnerSnapshot{}, ctx.Err()
	}
}

// AddNodes queues new nodes to be added to the execution graph.
// It blocks until the event loop accepts the nodes or the context cancels,
// making it completely safe to call dynamically while the engine is running.
func (r *Runner) AddNodes(ctx context.Context, nodes ...*Node) error {
	select {
	case r.addNodeCh <- nodes:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Start runs the execution engine's main event loop.
// It processes queues, state snapshots, and dispatches readiness logic asynchronously.
// Start blocks until the context is canceled or a node returns an error ending the graph.
func (r *Runner) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

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

	finish := func(status RunnerStatus, runErr error) error {
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
			snap := RunnerSnapshot{
				CompletedNodes: make(map[string]any),
				PendingNodes:   make(map[string]int),
				GraphEdges:     make(map[string][]string),
				NodesData:      make(map[string]*Node),
				ActiveCount:    activeCount,
			}
			for k, v := range outputs {
				snap.CompletedNodes[k] = v
			}
			for k, v := range nodes {
				snap.NodesData[k] = v
			}
			for k, v := range pendingParents {
				snap.PendingNodes[k] = v
			}
			for p, cList := range children {
				var childIDs []string
				for _, c := range cList {
					childIDs = append(childIDs, c.Id)
				}
				snap.GraphEdges[p] = childIDs
			}
			replyCh <- snap
		}
	}
}
