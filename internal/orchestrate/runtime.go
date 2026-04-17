package orchestrate

import (
	"context"
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

// Runtime acts as an execution engine that schedules and runs Nodes based on dependencies.
// It manages state strictly inside a single event loop to safely support concurrency.
type Runtime struct {
	id     string
	logger *slog.Logger

	// Core channels for the event loop
	addNodeCh chan []*Node
	resultCh  chan executionResult
	queryCh   chan chan<- RuntimeSnapshot

	// Subscription mechanisms
	subscribers []chan<- RuntimeEvent
	subMutex    sync.RWMutex
}

// NewRuntime initializes a new Runtime instance.
func NewRuntime(logger *slog.Logger) *Runtime {
	return &Runtime{
		id:          shortuuid.NewWithNamespace("http://github.com/tsumina/dango"),
		logger:      logger,
		addNodeCh:   make(chan []*Node),
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

// Start runs the execution engine's main event loop.
// It processes queues, state snapshots, and dispatches readiness logic asynchronously.
// Start blocks until the context is canceled or a node returns an error ending the graph.
func (r *Runtime) Start(ctx context.Context) error {
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

	defer broadcast(RuntimeEvent{Type: EventEngineStopped})

	runNode := func(n *Node) {
		inputs := make(map[string]any)
		for _, p := range n.Parents {
			inputs[p.Id] = outputs[p.Id]
		}

		n.UpdatedAt = time.Now()
		activeCount++
		broadcast(RuntimeEvent{Type: EventNodeStarted, NodeID: n.Id})

		go func() {
			out, dynNodes, err := n.Executor.Execute(ctx, inputs)
			select {
			case <-ctx.Done():
			case r.resultCh <- executionResult{nodeID: n.Id, output: out, newNodes: dynNodes, err: err}:
			}
		}()
	}

	addNodesInternal := func(newNodes []*Node) {
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

			broadcast(RuntimeEvent{Type: EventNodeAdded, NodeID: n.Id})

			if pending == 0 {
				runNode(n)
			}
		}
	}

	// Main Non-blocking Event Loop
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case newNodes := <-r.addNodeCh:
			addNodesInternal(newNodes)

		case res := <-r.resultCh:
			n := nodes[res.nodeID]
			n.UpdatedAt = time.Now()
			n.FinishedAt = n.UpdatedAt
			activeCount--

			if res.err != nil {
				r.logger.Error("Node execution failed, terminating chain.", "node_id", res.nodeID, "error", res.err)
				broadcast(RuntimeEvent{Type: EventNodeFailed, NodeID: res.nodeID, Data: res.err})
				return res.err // Extinguish engine on first error
			}

			// Inject dynamically spawned nodes to continue calculation paths seamlessly
			addNodesInternal(res.newNodes)

			outputs[res.nodeID] = res.output
			broadcast(RuntimeEvent{Type: EventNodeCompleted, NodeID: res.nodeID, Data: res.output})

			for _, child := range children[res.nodeID] {
				pendingParents[child.Id]--
				if pendingParents[child.Id] == 0 {
					runNode(child)
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
