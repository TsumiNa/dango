package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lithammer/shortuuid/v4"
)

// EventType defines the lifecycle events published by the runtime.
type EventType uint8

const (
	EventNodeAdded EventType = iota
	EventNodeStarted
	EventNodeCompleted
	EventNodeFailed
	EventEngineIdle
	EventEngineStopped
)

func (e EventType) String() string {
	switch e {
	case EventNodeAdded:
		return "NodeAdded"
	case EventNodeStarted:
		return "NodeStarted"
	case EventNodeCompleted:
		return "NodeCompleted"
	case EventNodeFailed:
		return "NodeFailed"
	case EventEngineIdle:
		return "EngineIdle"
	case EventEngineStopped:
		return "EngineStopped"
	default:
		return "Unknown"
	}
}

// RuntimeEvent represents a notification regarding graph execution states.
type RuntimeEvent struct {
	Type   EventType
	NodeID string
	Data   any // Output for Completed, err for Failed
}

// RuntimeSnapshot is a queryable freeze-frame of the engine's state.
type RuntimeSnapshot struct {
	ActiveCount    int
	CompletedNodes map[string]any
	PendingNodes   map[string]int
	GraphEdges     map[string][]string // parent -> children IDs
	NodesData      map[string]*Node    // Make nodes accessible for snapshot reading
}

// RuntimeStatus reports the current lifecycle state of a Runtime.
type RuntimeStatus string

const (
	RuntimeStatusPending  RuntimeStatus = "pending"
	RuntimeStatusRunning  RuntimeStatus = "running"
	RuntimeStatusIdle     RuntimeStatus = "idle"
	RuntimeStatusFailed   RuntimeStatus = "failed"
	RuntimeStatusCanceled RuntimeStatus = "canceled"
)

// RuntimeState is the externally visible lifecycle snapshot of a Runtime.
type RuntimeState struct {
	Status     RuntimeStatus `json:"status" yaml:"status"`
	StartedAt  time.Time     `json:"started_at,omitempty" yaml:"started_at,omitempty"`
	UpdatedAt  time.Time     `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
	FinishedAt time.Time     `json:"finished_at,omitempty" yaml:"finished_at,omitempty"`
	Error      string        `json:"error,omitempty" yaml:"error,omitempty"`
}

// Node represents a single unit of work within the Runtime's execution graph.
type Node struct {
	Id      string  `json:"id" yaml:"id"`
	Parents []*Node `json:"parents,omitempty" yaml:"parents,omitempty"`
	// Executor contains the execution logic of the node.
	Executor *Executor `json:"-" yaml:"-"`

	CreatedAt  time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt  time.Time `json:"updated_at" yaml:"updated_at"`
	FinishedAt time.Time `json:"finished_at" yaml:"finished_at"`
}

type executionResult struct {
	nodeID   string
	output   any
	newNodes []*Node
	err      error
}

// ErrRuntimeAlreadyStarted is returned when callers attempt to start or
// configure persistence on a Runtime that has already started.
var ErrRuntimeAlreadyStarted = errors.New("orchestrate: runtime already started")

// Runtime acts as an execution engine that schedules and runs Nodes based on dependencies.
// It manages state strictly inside a single event loop to safely support concurrency.
type Runtime struct {
	id     string
	logger *slog.Logger

	stateMu sync.RWMutex
	state   RuntimeState
	store   RuntimeStore

	// Core channels for the event loop
	addNodeCh chan []*Node
	resultCh  chan executionResult
	queryCh   chan chan<- RuntimeSnapshot

	// Subscription mechanisms
	subscribers []chan<- RuntimeEvent
	subMutex    sync.RWMutex
}

// ID returns the stable identifier assigned to this Runtime at creation time.
func (r *Runtime) ID() string { return r.id }

// State returns the current lifecycle snapshot of the Runtime.
func (r *Runtime) State() RuntimeState {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return r.state
}

// SetStore configures an append-only persistence store for the Runtime.
//
// It may only be called before [Runtime.Start]. Passing nil clears any
// previously configured store.
func (r *Runtime) SetStore(store RuntimeStore) error {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.state.Status != RuntimeStatusPending {
		return ErrRuntimeAlreadyStarted
	}
	r.store = store
	return nil
}

// NewRuntime initializes a new Runtime instance.
func NewRuntime(logger *slog.Logger) *Runtime {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runtime{
		id:          shortuuid.New(),
		logger:      logger,
		state:       RuntimeState{Status: RuntimeStatusPending},
		addNodeCh:   make(chan []*Node, 1),
		resultCh:    make(chan executionResult),
		queryCh:     make(chan chan<- RuntimeSnapshot),
		subscribers: make([]chan<- RuntimeEvent, 0),
	}
}

// Subscribe returns a channel to receive runtime lifecycle events dynamically.
// It acts as a publish-subscribe mechanism for upper layers.
func (r *Runtime) Subscribe(bufferSize int) <-chan RuntimeEvent {
	ch := make(chan RuntimeEvent, bufferSize)
	r.subMutex.Lock()
	defer r.subMutex.Unlock()
	r.subscribers = append(r.subscribers, ch)
	return ch
}

// GetSnapshot safely queries the current graph and execution state.
// It blocks until the event loop replies or the given context cancels.
func (r *Runtime) GetSnapshot(ctx context.Context) (RuntimeSnapshot, error) {
	replyCh := make(chan RuntimeSnapshot, 1)
	select {
	case r.queryCh <- replyCh:
		return <-replyCh, nil
	case <-ctx.Done():
		return RuntimeSnapshot{}, ctx.Err()
	}
}

// AddNodes queues new nodes to be added to the execution graph.
// It blocks until the event loop accepts the nodes or the context cancels,
// making it completely safe to call dynamically while the engine is running.
func (r *Runtime) AddNodes(ctx context.Context, nodes ...*Node) error {
	select {
	case r.addNodeCh <- nodes:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) transitionState(status RuntimeStatus, runErr error, terminal bool) (RuntimeState, bool) {
	now := time.Now()
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}

	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	next := r.state
	if next.Status == status && next.Error == errText && (!terminal || !next.FinishedAt.IsZero()) {
		return next, false
	}
	if next.StartedAt.IsZero() && status != RuntimeStatusPending {
		next.StartedAt = now
	}
	next.Status = status
	next.UpdatedAt = now
	next.Error = errText
	if terminal {
		next.FinishedAt = now
	}
	r.state = next
	return next, true
}

func (r *Runtime) appendRecord(store RuntimeStore, rec *RuntimeRecord) error {
	if store == nil {
		return nil
	}
	if _, err := store.Append(context.Background(), r.id, rec); err != nil {
		return fmt.Errorf("orchestrate: persist runtime %q: %w", r.id, err)
	}
	return nil
}

func (r *Runtime) recordState(store RuntimeStore, status RuntimeStatus, runErr error, terminal bool) error {
	state, changed := r.transitionState(status, runErr, terminal)
	if !changed {
		return nil
	}
	return r.appendRecord(store, &RuntimeRecord{Kind: RuntimeRecordStatus, Status: state.Status, Error: state.Error})
}

// Start runs the execution engine's main event loop.
// It processes queues, state snapshots, and dispatches readiness logic asynchronously.
// Start blocks until the context is canceled or a node returns an error ending the graph.
func (r *Runtime) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	r.stateMu.Lock()
	if r.state.Status != RuntimeStatusPending {
		r.stateMu.Unlock()
		return ErrRuntimeAlreadyStarted
	}
	store := r.store
	now := time.Now()
	r.state = RuntimeState{
		Status:    RuntimeStatusRunning,
		StartedAt: now,
		UpdatedAt: now,
	}
	initialState := r.state
	r.stateMu.Unlock()

	if err := r.appendRecord(store, &RuntimeRecord{Kind: RuntimeRecordInit}); err != nil {
		_, _ = r.transitionState(RuntimeStatusFailed, err, true)
		return err
	}
	if err := r.appendRecord(store, &RuntimeRecord{Kind: RuntimeRecordStatus, Status: initialState.Status}); err != nil {
		_, _ = r.transitionState(RuntimeStatusFailed, err, true)
		return err
	}

	r.logger.Info("Starting execution engine event loop...")

	// Inherit cancellation so we can abort running nodes if the engine stops
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	outputs := make(map[string]any)
	pendingParents := make(map[string]int)
	nodes := make(map[string]*Node)
	children := make(map[string][]*Node)
	activeCount := 0

	broadcast := func(event RuntimeEvent) {
		r.subMutex.RLock()
		defer r.subMutex.RUnlock()
		for _, ch := range r.subscribers {
			select {
			case ch <- event:
			default: // skip if subscriber channel is full/blocked to prevent stalling the loop
			}
		}
	}

	emitEvent := func(event RuntimeEvent) error {
		if err := r.appendRecord(store, &RuntimeRecord{Kind: RuntimeRecordEvent, Event: newStoredRuntimeEvent(event)}); err != nil {
			return err
		}
		broadcast(event)
		return nil
	}

	finish := func(status RuntimeStatus, runErr error) error {
		statusErr := r.recordState(store, status, runErr, true)
		stopErr := emitEvent(RuntimeEvent{Type: EventEngineStopped})
		return errors.Join(runErr, statusErr, stopErr)
	}

	runNode := func(n *Node) error {
		inputs := make(map[string]any)
		for _, p := range n.Parents {
			inputs[p.Id] = outputs[p.Id]
		}

		n.UpdatedAt = time.Now()
		activeCount++
		if err := r.recordState(store, RuntimeStatusRunning, nil, false); err != nil {
			activeCount--
			return err
		}
		if err := emitEvent(RuntimeEvent{Type: EventNodeStarted, NodeID: n.Id}); err != nil {
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

			if err := emitEvent(RuntimeEvent{Type: EventNodeAdded, NodeID: n.Id}); err != nil {
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

	// Main Non-blocking Event Loop
	for {
		select {
		case <-ctx.Done():
			return finish(RuntimeStatusCanceled, ctx.Err())

		case newNodes := <-r.addNodeCh:
			if err := addNodesInternal(newNodes); err != nil {
				return finish(RuntimeStatusFailed, err)
			}

		case res := <-r.resultCh:
			n := nodes[res.nodeID]
			n.UpdatedAt = time.Now()
			n.FinishedAt = n.UpdatedAt
			activeCount--

			if res.err != nil {
				r.logger.Error("Node execution failed, terminating chain.", "node_id", res.nodeID, "error", res.err)
				eventErr := emitEvent(RuntimeEvent{Type: EventNodeFailed, NodeID: res.nodeID, Data: res.err})
				return finish(RuntimeStatusFailed, errors.Join(res.err, eventErr))
			}

			// Inject dynamically spawned nodes to continue calculation paths seamlessly
			if err := addNodesInternal(res.newNodes); err != nil {
				return finish(RuntimeStatusFailed, err)
			}

			outputs[res.nodeID] = res.output
			if err := emitEvent(RuntimeEvent{Type: EventNodeCompleted, NodeID: res.nodeID, Data: res.output}); err != nil {
				return finish(RuntimeStatusFailed, err)
			}

			for _, child := range children[res.nodeID] {
				pendingParents[child.Id]--
				if pendingParents[child.Id] == 0 {
					if err := runNode(child); err != nil {
						return finish(RuntimeStatusFailed, err)
					}
				}
			}
			if activeCount == 0 {
				if err := r.recordState(store, RuntimeStatusIdle, nil, false); err != nil {
					return finish(RuntimeStatusFailed, err)
				}
				if err := emitEvent(RuntimeEvent{Type: EventEngineIdle}); err != nil {
					return finish(RuntimeStatusFailed, err)
				}
			}

		case replyCh := <-r.queryCh:
			snap := RuntimeSnapshot{
				CompletedNodes: make(map[string]any),
				PendingNodes:   make(map[string]int),
				GraphEdges:     make(map[string][]string),
				NodesData:      make(map[string]*Node),
				ActiveCount:    activeCount,
			}
			// Copy internal maps safely for snapshot reading
			for k, v := range outputs {
				snap.CompletedNodes[k] = v
			}
			for k, v := range nodes {
				// shallow copy the node struct reference
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
