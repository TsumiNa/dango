package orchestrate

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tsumina/dango/internal/llm/skill"
)

var (
	defaultOrchestrator     *Orchestrator
	defaultOrchestratorOnce sync.Once
)

// ErrRuntimeNotFound is returned when an Orchestrator runtime lookup misses.
var ErrRuntimeNotFound = errors.New("orchestrate: runtime not found")

// ErrRuntimeActive is returned when callers attempt to remove a runtime that
// is still live and may continue to accept work.
var ErrRuntimeActive = errors.New("orchestrate: runtime is still active")

// ErrRuntimeStoreNotConfigured is returned when persisted runtime records are
// requested without a configured runtime store.
var ErrRuntimeStoreNotConfigured = errors.New("orchestrate: runtime store not configured")

// PlanningFunc analyzes req against the registered skills and returns either a
// coarse plan or a structured reason the task cannot proceed.
type PlanningFunc func(req *Request, skills map[string]*skill.Skill) (*CoarsePlan, *RejectReason, error)

// RequestPriority orders queued StartRequest submissions.
//
// Valid priorities are the integers 0 through 4 inclusive. The zero value is
// the default priority, and larger values run first when the Orchestrator is
// throttling concurrent runtime execution.
type RequestPriority int

const (
	RequestPriorityDefault RequestPriority = 0
	RequestPriorityHighest RequestPriority = 4
)

func (p RequestPriority) valid() bool {
	return p >= RequestPriorityDefault && p <= RequestPriorityHighest
}

// Orchestrator is the singleton runtime factory that bridges external user
// requests to runtime assembly.
//
// It keeps a registry of lightweight skills loaded through [skill.Load], asks a
// planner to convert a request into a coarse execution plan, and materializes a
// fresh [Runtime] plus its [Executor] graph for each accepted plan.
type Orchestrator struct {
	logger *slog.Logger

	mu                 sync.RWMutex
	configLocked       bool
	runtimeStore       RuntimeStore
	maxRunningRuntimes int
	skills             map[string]*skill.Skill
	runtimes           map[string]*ManagedRuntime
	runningRuntimeIDs  map[string]struct{}
	queuedRuntimeByID  map[string]*queuedRuntime
	queuedRuntimes     runtimeStartQueue
	nextQueueOrder     uint64
	planFn             PlanningFunc
}

// Request is the external task description the Orchestrator receives from the
// caller.
type Request struct {
	Input    string          `json:"input" yaml:"input"`
	Priority RequestPriority `json:"priority,omitempty" yaml:"priority,omitempty"`
}

// CoarsePlan is the Orchestrator's high-level task graph before execution
// starts.
type CoarsePlan struct {
	Request   string           `json:"request" yaml:"request"`
	RuntimeID string           `json:"runtime_id,omitempty" yaml:"runtime_id,omitempty"`
	Nodes     []CoarsePlanNode `json:"nodes" yaml:"nodes"`
}

// CoarsePlanNode describes one executor-sized unit in a coarse plan.
type CoarsePlanNode struct {
	ID              string   `json:"id" yaml:"id"`
	SkillName       string   `json:"skill_name" yaml:"skill_name"`
	TaskDescription string   `json:"task_description" yaml:"task_description"`
	DependsOn       []string `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
}

// RejectReason explains why a request cannot currently be turned into a plan.
type RejectReason struct {
	Summary       string   `json:"summary" yaml:"summary"`
	Analysis      string   `json:"analysis" yaml:"analysis"`
	MissingSkills []string `json:"missing_skills,omitempty" yaml:"missing_skills,omitempty"`
}

// ManagedRuntime is the Orchestrator-owned runtime record created for a plan.
//
// Runtime is the execution engine that will eventually run the graph, Plan is
// the coarse plan that produced it, and Nodes is the materialized node graph
// keyed by node ID for later inspection or startup.
type ManagedRuntime struct {
	Runtime *Runtime
	Plan    *CoarsePlan
	Nodes   map[string]*Node

	mu                   sync.RWMutex
	snapshot             RuntimeSnapshot
	started              bool
	stoppedEventSeen     bool
	cancel               context.CancelFunc
	subscribers          map[uint64]chan RuntimeUpdate
	nextSubscriberID     uint64
	onExecutionDrained   func()
	executionDrainedOnce sync.Once
}

// RuntimeView is the query-facing snapshot Orchestrator exposes for one
// managed runtime.
type RuntimeView struct {
	RuntimeID string          `json:"runtime_id" yaml:"runtime_id"`
	Plan      *CoarsePlan     `json:"plan,omitempty" yaml:"plan,omitempty"`
	State     RuntimeState    `json:"state" yaml:"state"`
	Snapshot  RuntimeSnapshot `json:"snapshot" yaml:"snapshot"`
}

// RuntimeUpdate is the stream-facing update Orchestrator forwards to outer
// callers as a runtime changes state.
type RuntimeUpdate struct {
	RuntimeID string          `json:"runtime_id" yaml:"runtime_id"`
	State     RuntimeState    `json:"state" yaml:"state"`
	Snapshot  RuntimeSnapshot `json:"snapshot" yaml:"snapshot"`
	Event     *RuntimeEvent   `json:"event,omitempty" yaml:"event,omitempty"`
}

// Default returns the process-wide Orchestrator singleton.
//
// The singleton is always initialized with [slog.Default]. Callers that need a
// different logger can update it afterwards through [Orchestrator.SetLogger].
func Default() *Orchestrator {
	defaultOrchestratorOnce.Do(func() {
		defaultOrchestrator = newOrchestrator(nil)
	})
	return defaultOrchestrator
}

func newOrchestrator(logger *slog.Logger) *Orchestrator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Orchestrator{
		logger:            logger,
		skills:            make(map[string]*skill.Skill),
		runtimes:          make(map[string]*ManagedRuntime),
		runningRuntimeIDs: make(map[string]struct{}),
		queuedRuntimeByID: make(map[string]*queuedRuntime),
		planFn:            rejectUnconfiguredPlan,
	}
}

// SetLogger replaces the Orchestrator logger.
//
// Passing nil restores [slog.Default] so the Orchestrator and any Runtime it
// assembles remain usable. It can only be called before the first planning
// call.
func (o *Orchestrator) SetLogger(logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.configLocked {
		return fmt.Errorf("orchestrate: SetLogger can only be called during startup")
	}
	o.logger = logger
	return nil
}

// SetPlanningFunc replaces the planning function used by the Orchestrator's
// internal planning step.
// Passing nil restores the default planner, which rejects the request with a
// structured explanation until a real planner is wired in. It can only be
// called before the first planning call.
func (o *Orchestrator) SetPlanningFunc(planFn PlanningFunc) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.configLocked {
		return fmt.Errorf("orchestrate: SetPlanningFunc can only be called during startup")
	}
	if planFn == nil {
		o.planFn = rejectUnconfiguredPlan
		return nil
	}
	o.planFn = planFn
	return nil
}

// SetRuntimeStore configures the persistence store that newly assembled
// Runtimes should use.
//
// Passing nil clears any previously configured store. Like the other
// Orchestrator configuration entry points, it can only be called during
// startup.
func (o *Orchestrator) SetRuntimeStore(store RuntimeStore) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.configLocked {
		return fmt.Errorf("orchestrate: SetRuntimeStore can only be called during startup")
	}
	o.runtimeStore = store
	return nil
}

// SetMaxRunningRuntimes configures how many runtimes may execute at once for
// StartRequest submissions.
//
// A limit of zero disables throttling and starts every submitted request
// immediately. The limit is startup-only like the other orchestrator-wide
// execution settings.
func (o *Orchestrator) SetMaxRunningRuntimes(limit int) error {
	if limit < 0 {
		return fmt.Errorf("orchestrate: max running runtimes must be non-negative")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.configLocked {
		return fmt.Errorf("orchestrate: SetMaxRunningRuntimes can only be called during startup")
	}
	o.maxRunningRuntimes = limit
	return nil
}

// RegisterSkill loads dir through [skill.Load] and stores the resulting
// lightweight Skill in the registry under its declared name.
//
// Skills are runtime-managed state rather than startup-only configuration, so
// RegisterSkill may be called throughout the daemon lifetime.
func (o *Orchestrator) RegisterSkill(dir string) error {
	sk, err := skill.Load(dir)
	if err != nil {
		return err
	}
	if sk.Name == "" {
		return fmt.Errorf("orchestrate: registered skill in %q has empty name", dir)
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if _, exists := o.skills[sk.Name]; exists {
		return fmt.Errorf("orchestrate: skill %q already registered", sk.Name)
	}
	o.skills[sk.Name] = sk
	return nil
}

// RemoveSkill deletes the registered lightweight Skill identified by name.
//
// Like [RegisterSkill], RemoveSkill is allowed during the daemon lifetime so
// the available skill set can change while the Orchestrator is running.
func (o *Orchestrator) RemoveSkill(name string) error {
	if name == "" {
		return fmt.Errorf("orchestrate: RemoveSkill requires a non-empty skill name")
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if _, exists := o.skills[name]; !exists {
		return fmt.Errorf("orchestrate: skill %q is not registered", name)
	}
	delete(o.skills, name)
	return nil
}

// Skills returns a snapshot of the currently registered lightweight skill map.
func (o *Orchestrator) Skills() map[string]*skill.Skill {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return cloneSkillMap(o.skills)
}

// Runtimes returns a snapshot of the runtime records the Orchestrator has
// assembled so far.
func (o *Orchestrator) Runtimes() map[string]*ManagedRuntime {
	o.mu.RLock()
	defer o.mu.RUnlock()
	copyMap := make(map[string]*ManagedRuntime, len(o.runtimes))
	for id, runtime := range o.runtimes {
		copyMap[id] = runtime
	}
	return copyMap
}

// Runtime returns the managed runtime identified by id.
func (o *Orchestrator) Runtime(id string) (*ManagedRuntime, error) {
	if id == "" {
		return nil, fmt.Errorf("orchestrate: runtime id must not be empty")
	}

	o.mu.RLock()
	defer o.mu.RUnlock()
	runtime, ok := o.runtimes[id]
	if !ok {
		return nil, ErrRuntimeNotFound
	}
	return runtime, nil
}

// QueryRuntime returns the outer-facing view of the managed runtime
// identified by id.
func (o *Orchestrator) QueryRuntime(id string) (*RuntimeView, error) {
	runtime, err := o.Runtime(id)
	if err != nil {
		return nil, err
	}
	return runtime.view(), nil
}

// SubscribeRuntime returns a stream of runtime updates plus an unsubscribe
// function that stops further delivery.
func (o *Orchestrator) SubscribeRuntime(id string, bufferSize int) (<-chan RuntimeUpdate, func(), error) {
	runtime, err := o.Runtime(id)
	if err != nil {
		return nil, nil, err
	}
	ch, unsubscribe := runtime.subscribe(bufferSize)
	return ch, unsubscribe, nil
}

// LoadRuntimeRecords loads the persisted append-only record stream for the
// managed runtime identified by id.
func (o *Orchestrator) LoadRuntimeRecords(ctx context.Context, id string) ([]RuntimeRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return nil, fmt.Errorf("orchestrate: runtime id must not be empty")
	}

	o.mu.RLock()
	store := o.runtimeStore
	_, ok := o.runtimes[id]
	o.mu.RUnlock()
	if !ok {
		return nil, ErrRuntimeNotFound
	}
	if store == nil {
		return nil, ErrRuntimeStoreNotConfigured
	}
	return store.Load(ctx, id)
}

// RemoveRuntime removes a managed runtime from the Orchestrator and deletes its
// persisted log when one exists.
//
// Only pending, failed, or canceled runtimes may be removed. Running or idle
// runtimes are still live and therefore rejected.
func (o *Orchestrator) RemoveRuntime(ctx context.Context, id string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return fmt.Errorf("orchestrate: runtime id must not be empty")
	}

	o.mu.RLock()
	runtime, ok := o.runtimes[id]
	store := o.runtimeStore
	o.mu.RUnlock()
	if !ok {
		return ErrRuntimeNotFound
	}
	if !runtimeRemovable(runtime.Runtime.State()) {
		return ErrRuntimeActive
	}
	if store != nil {
		if err := store.Delete(ctx, id); err != nil && !errors.Is(err, ErrRuntimeLogNotFound) {
			return err
		}
	}

	o.mu.Lock()
	current, ok := o.runtimes[id]
	if !ok {
		o.mu.Unlock()
		return ErrRuntimeNotFound
	}
	if !runtimeRemovable(current.Runtime.State()) {
		o.mu.Unlock()
		return ErrRuntimeActive
	}
	if entry, ok := o.queuedRuntimeByID[id]; ok {
		entry.canceled = true
		delete(o.queuedRuntimeByID, id)
		entry.deactivate()
	}
	delete(o.runtimes, id)
	toStart, toCancel := o.collectQueuedStartsLocked()
	o.mu.Unlock()
	o.finishQueuedDispatch(toStart, toCancel)
	return nil
}

// StartRuntime starts the managed runtime identified by id and begins
// forwarding its status to query and stream consumers.
func (o *Orchestrator) StartRuntime(ctx context.Context, id string) error {
	runtime, err := o.Runtime(id)
	if err != nil {
		return err
	}
	return o.startManagedRuntime(ctx, runtime)
}

// StartRequest is the outer-facing request entrypoint.
//
// It plans and materializes a runtime, then either starts it immediately or
// queues it when the configured runtime execution limit is full. Query and
// stream APIs can be used afterwards with the returned RuntimeID.
func (o *Orchestrator) StartRequest(ctx context.Context, req *Request) (coarsePlan *CoarsePlan, rejectReason *RejectReason, err error) {
	if req == nil {
		return nil, nil, fmt.Errorf("orchestrate: nil request")
	}
	if !req.Priority.valid() {
		return nil, nil, fmt.Errorf("orchestrate: request priority must be between %d and %d", RequestPriorityDefault, RequestPriorityHighest)
	}
	plan, reject, err := o.planFromRequest(req)
	if err != nil || reject != nil || plan == nil {
		return plan, reject, err
	}
	runtime, err := o.Runtime(plan.RuntimeID)
	if err != nil {
		return nil, nil, err
	}
	if err := o.submitManagedRuntime(ctx, runtime, req.Priority); err != nil {
		return nil, nil, err
	}
	return plan, nil, nil
}

// planFromRequest asks the configured planner to analyze req against the
// registered skills.
//
// When the planner rejects the request, planFromRequest returns the rejection
// details without creating a runtime. When the planner returns a coarse plan,
// planFromRequest materializes the plan into Executors and Nodes, creates a new
// Runtime for them, stores that runtime inside the Orchestrator, and returns
// the plan annotated with the runtime ID.
func (o *Orchestrator) planFromRequest(req *Request) (coarsePlan *CoarsePlan, rejectReason *RejectReason, err error) {
	if req == nil {
		return nil, nil, fmt.Errorf("orchestrate: nil request")
	}

	o.mu.Lock()
	o.configLocked = true
	logger := o.logger
	planFn := o.planFn
	runtimeStore := o.runtimeStore
	skills := cloneSkillMap(o.skills)
	o.mu.Unlock()

	plan, reject, err := planFn(req, skills)
	if err != nil {
		return nil, nil, err
	}
	if plan != nil && reject != nil {
		return nil, nil, fmt.Errorf("orchestrate: planner returned both a plan and a reject reason")
	}
	if reject != nil {
		return nil, reject, nil
	}
	if plan == nil {
		return nil, nil, fmt.Errorf("orchestrate: planner returned neither a plan nor a reject reason")
	}

	managedRuntime, err := buildManagedRuntime(logger, runtimeStore, req, plan, skills)
	if err != nil {
		return nil, nil, err
	}
	managedRuntime.onExecutionDrained = func() {
		o.releaseRuntimeExecutionSlot(managedRuntime.Runtime.ID())
	}
	plan.RuntimeID = managedRuntime.Runtime.ID()

	o.mu.Lock()
	o.runtimes[plan.RuntimeID] = managedRuntime
	o.mu.Unlock()

	return plan, nil, nil
}

func (o *Orchestrator) submitManagedRuntime(ctx context.Context, runtime *ManagedRuntime, priority RequestPriority) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		runtime.finishBeforeStart(err)
		return err
	}

	id := runtime.Runtime.ID()
	o.mu.Lock()
	if o.maxRunningRuntimes == 0 || len(o.runningRuntimeIDs) < o.maxRunningRuntimes {
		o.runningRuntimeIDs[id] = struct{}{}
		o.mu.Unlock()
		return o.startManagedRuntimeWithReservedSlot(ctx, runtime)
	}
	entry := &queuedRuntime{
		runtime:  runtime,
		ctx:      ctx,
		priority: priority,
		order:    o.nextQueueOrder,
		done:     make(chan struct{}),
	}
	o.nextQueueOrder++
	heap.Push(&o.queuedRuntimes, entry)
	o.queuedRuntimeByID[id] = entry
	o.mu.Unlock()

	go o.watchQueuedRuntime(entry)
	return nil
}

func (o *Orchestrator) watchQueuedRuntime(entry *queuedRuntime) {
	select {
	case <-entry.done:
		return
	case <-entry.ctx.Done():
		o.cancelQueuedRuntime(entry, entry.ctx.Err())
	}
}

func (o *Orchestrator) cancelQueuedRuntime(entry *queuedRuntime, runErr error) {
	toStart, toCancel := o.removeQueuedRuntime(entry, true)
	entry.runtime.finishBeforeStart(runErr)
	o.finishQueuedDispatch(toStart, toCancel)
}

func (o *Orchestrator) removeQueuedRuntime(entry *queuedRuntime, canceled bool) ([]*queuedRuntime, []*queuedRuntime) {
	id := entry.runtime.Runtime.ID()
	o.mu.Lock()
	current, ok := o.queuedRuntimeByID[id]
	if !ok || current != entry {
		o.mu.Unlock()
		return nil, nil
	}
	if canceled {
		entry.canceled = true
	}
	delete(o.queuedRuntimeByID, id)
	entry.deactivate()
	toStart, toCancel := o.collectQueuedStartsLocked()
	o.mu.Unlock()
	return toStart, toCancel
}

func (o *Orchestrator) startManagedRuntime(ctx context.Context, runtime *ManagedRuntime) error {
	id := runtime.Runtime.ID()
	o.mu.Lock()
	if entry, ok := o.queuedRuntimeByID[id]; ok {
		entry.canceled = true
		delete(o.queuedRuntimeByID, id)
		entry.deactivate()
	}
	if _, ok := o.runningRuntimeIDs[id]; !ok {
		o.runningRuntimeIDs[id] = struct{}{}
	}
	o.mu.Unlock()
	return o.startManagedRuntimeWithReservedSlot(ctx, runtime)
}

func (o *Orchestrator) startManagedRuntimeWithReservedSlot(ctx context.Context, runtime *ManagedRuntime) error {
	if err := runtime.start(ctx); err != nil {
		o.handleManagedRuntimeStartError(runtime, err)
		return err
	}
	return nil
}

func (o *Orchestrator) handleManagedRuntimeStartError(runtime *ManagedRuntime, runErr error) {
	id := runtime.Runtime.ID()
	o.mu.Lock()
	delete(o.runningRuntimeIDs, id)
	toStart, toCancel := o.collectQueuedStartsLocked()
	o.mu.Unlock()
	if runtime.Runtime.State().Status == RuntimeStatusPending {
		runtime.finishBeforeStart(runErr)
	}
	o.finishQueuedDispatch(toStart, toCancel)
}

func (o *Orchestrator) releaseRuntimeExecutionSlot(id string) {
	o.mu.Lock()
	delete(o.runningRuntimeIDs, id)
	toStart, toCancel := o.collectQueuedStartsLocked()
	o.mu.Unlock()
	o.finishQueuedDispatch(toStart, toCancel)
}

func (o *Orchestrator) collectQueuedStartsLocked() ([]*queuedRuntime, []*queuedRuntime) {
	var toStart []*queuedRuntime
	var toCancel []*queuedRuntime
	for o.maxRunningRuntimes == 0 || len(o.runningRuntimeIDs) < o.maxRunningRuntimes {
		entry := o.popQueuedRuntimeLocked()
		if entry == nil {
			break
		}
		delete(o.queuedRuntimeByID, entry.runtime.Runtime.ID())
		entry.deactivate()
		if entry.canceled {
			continue
		}
		if err := entry.ctx.Err(); err != nil {
			entry.canceled = true
			toCancel = append(toCancel, entry)
			continue
		}
		o.runningRuntimeIDs[entry.runtime.Runtime.ID()] = struct{}{}
		toStart = append(toStart, entry)
	}
	return toStart, toCancel
}

func (o *Orchestrator) popQueuedRuntimeLocked() *queuedRuntime {
	for len(o.queuedRuntimes) > 0 {
		entry := heap.Pop(&o.queuedRuntimes).(*queuedRuntime)
		if entry.canceled {
			continue
		}
		return entry
	}
	return nil
}

func (o *Orchestrator) finishQueuedDispatch(toStart []*queuedRuntime, toCancel []*queuedRuntime) {
	for _, entry := range toCancel {
		entry.runtime.finishBeforeStart(entry.ctx.Err())
	}
	for _, entry := range toStart {
		if err := o.startManagedRuntimeWithReservedSlot(entry.ctx, entry.runtime); err != nil {
			continue
		}
	}
}

func buildManagedRuntime(logger *slog.Logger, store RuntimeStore, req *Request, plan *CoarsePlan, skills map[string]*skill.Skill) (*ManagedRuntime, error) {
	if len(plan.Nodes) == 0 {
		return nil, fmt.Errorf("orchestrate: coarse plan must contain at least one node")
	}

	nodes := make(map[string]*Node, len(plan.Nodes))
	for _, step := range plan.Nodes {
		if step.ID == "" {
			return nil, fmt.Errorf("orchestrate: coarse plan node has empty id")
		}
		if _, exists := nodes[step.ID]; exists {
			return nil, fmt.Errorf("orchestrate: coarse plan node %q is duplicated", step.ID)
		}
		if step.SkillName == "" {
			return nil, fmt.Errorf("orchestrate: coarse plan node %q has empty skill name", step.ID)
		}
		sk, ok := skills[step.SkillName]
		if !ok {
			return nil, fmt.Errorf("orchestrate: coarse plan node %q references unregistered skill %q", step.ID, step.SkillName)
		}

		planner := &ExecutionPlanner{
			id:              step.ID,
			TaskDescription: step.TaskDescription,
		}
		if planner.TaskDescription == "" {
			planner.TaskDescription = req.Input
		}
		executor, err := NewExecutor(logger, sk, planner)
		if err != nil {
			return nil, fmt.Errorf("orchestrate: build executor for node %q: %w", step.ID, err)
		}

		nodes[step.ID] = &Node{
			Id:       step.ID,
			Executor: executor,
		}
	}

	for _, step := range plan.Nodes {
		node := nodes[step.ID]
		for _, parentID := range step.DependsOn {
			parent, ok := nodes[parentID]
			if !ok {
				return nil, fmt.Errorf("orchestrate: coarse plan node %q depends on unknown node %q", step.ID, parentID)
			}
			node.Parents = append(node.Parents, parent)
		}
	}

	runtime := NewRuntime(logger)
	if err := runtime.SetStore(store); err != nil {
		return nil, fmt.Errorf("orchestrate: configure runtime store: %w", err)
	}
	return newManagedRuntime(runtime, plan, nodes), nil
}

func cloneSkillMap(skills map[string]*skill.Skill) map[string]*skill.Skill {
	copyMap := make(map[string]*skill.Skill, len(skills))
	for name, sk := range skills {
		copyMap[name] = sk
	}
	return copyMap
}

func runtimeRemovable(state RuntimeState) bool {
	switch state.Status {
	case RuntimeStatusPending, RuntimeStatusFailed, RuntimeStatusCanceled:
		return true
	default:
		return false
	}
}

func newManagedRuntime(runtime *Runtime, plan *CoarsePlan, nodes map[string]*Node) *ManagedRuntime {
	return &ManagedRuntime{
		Runtime:     runtime,
		Plan:        cloneCoarsePlan(plan),
		Nodes:       nodes,
		snapshot:    buildInitialRuntimeSnapshot(nodes),
		subscribers: make(map[uint64]chan RuntimeUpdate),
	}
}

func (m *ManagedRuntime) view() *RuntimeView {
	m.mu.RLock()
	snapshot := cloneRuntimeSnapshot(m.snapshot)
	plan := cloneCoarsePlan(m.Plan)
	m.mu.RUnlock()
	return &RuntimeView{
		RuntimeID: m.Runtime.ID(),
		Plan:      plan,
		State:     m.Runtime.State(),
		Snapshot:  snapshot,
	}
}

func (m *ManagedRuntime) subscribe(bufferSize int) (<-chan RuntimeUpdate, func()) {
	if bufferSize < 1 {
		bufferSize = 1
	}
	ch := make(chan RuntimeUpdate, bufferSize)

	m.mu.Lock()
	update := RuntimeUpdate{
		RuntimeID: m.Runtime.ID(),
		State:     m.Runtime.State(),
		Snapshot:  cloneRuntimeSnapshot(m.snapshot),
	}
	terminal := runtimeTerminal(update.State)
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
		if sub, ok := m.subscribers[id]; ok {
			delete(m.subscribers, id)
			close(sub)
		}
	}
	return ch, unsubscribe
}

func (m *ManagedRuntime) start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return ErrRuntimeAlreadyStarted
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.started = true
	m.cancel = cancel
	rootNodes := make([]*Node, 0, len(m.Nodes))
	for _, node := range m.Nodes {
		rootNodes = append(rootNodes, node)
	}
	events := m.Runtime.Subscribe(64)
	m.mu.Unlock()

	if err := m.Runtime.AddNodes(runCtx, rootNodes...); err != nil {
		cancel()
		m.mu.Lock()
		m.started = false
		m.cancel = nil
		m.mu.Unlock()
		return err
	}

	go m.forwardRuntimeEvents(events)
	go m.runRuntime(runCtx)
	return nil
}

func (m *ManagedRuntime) runRuntime(ctx context.Context) {
	err := m.Runtime.Start(ctx)
	m.mu.Lock()
	stoppedSeen := m.stoppedEventSeen
	m.cancel = nil
	m.mu.Unlock()
	if !stoppedSeen {
		m.publishUpdate(nil)
	}
	if runtimeTerminal(m.Runtime.State()) {
		m.closeSubscribers()
	}
	_ = err
}

func (m *ManagedRuntime) forwardRuntimeEvents(events <-chan RuntimeEvent) {
	for event := range events {
		ev := event
		m.publishUpdate(&ev)
		if ev.Type == EventEngineStopped {
			return
		}
	}
}

func (m *ManagedRuntime) publishUpdate(event *RuntimeEvent) {
	state := m.Runtime.State()
	snapshot := m.currentSnapshot(event)
	shouldDrain := state.Status == RuntimeStatusIdle || runtimeTerminal(state)
	update := RuntimeUpdate{
		RuntimeID: m.Runtime.ID(),
		State:     state,
		Snapshot:  snapshot,
		Event:     event,
	}

	m.mu.Lock()
	m.snapshot = cloneRuntimeSnapshot(snapshot)
	if event != nil && event.Type == EventEngineStopped {
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

func (m *ManagedRuntime) finishBeforeStart(runErr error) {
	status := runtimeStatusFromStartError(runErr)
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	now := time.Now()
	m.Runtime.stateMu.Lock()
	m.Runtime.state = RuntimeState{
		Status:     status,
		UpdatedAt:  now,
		FinishedAt: now,
		Error:      errText,
	}
	m.Runtime.stateMu.Unlock()
	m.publishUpdate(nil)
	m.closeSubscribers()
}

func (m *ManagedRuntime) currentSnapshot(event *RuntimeEvent) RuntimeSnapshot {
	if event == nil || event.Type == EventEngineStopped {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return cloneRuntimeSnapshot(m.snapshot)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	snapshot, err := m.Runtime.GetSnapshot(ctx)
	if err != nil {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return cloneRuntimeSnapshot(m.snapshot)
	}
	return snapshot
}

func (m *ManagedRuntime) closeSubscribers() {
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

func buildInitialRuntimeSnapshot(nodes map[string]*Node) RuntimeSnapshot {
	snapshot := RuntimeSnapshot{
		CompletedNodes: make(map[string]any),
		PendingNodes:   make(map[string]int, len(nodes)),
		GraphEdges:     make(map[string][]string),
		NodesData:      make(map[string]*Node, len(nodes)),
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

func cloneRuntimeSnapshot(snapshot RuntimeSnapshot) RuntimeSnapshot {
	copySnapshot := RuntimeSnapshot{
		ActiveCount:    snapshot.ActiveCount,
		CompletedNodes: make(map[string]any, len(snapshot.CompletedNodes)),
		PendingNodes:   make(map[string]int, len(snapshot.PendingNodes)),
		GraphEdges:     make(map[string][]string, len(snapshot.GraphEdges)),
		NodesData:      make(map[string]*Node, len(snapshot.NodesData)),
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

func runtimeTerminal(state RuntimeState) bool {
	switch state.Status {
	case RuntimeStatusFailed, RuntimeStatusCanceled:
		return true
	default:
		return false
	}
}

func runtimeStatusFromStartError(runErr error) RuntimeStatus {
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return RuntimeStatusCanceled
	}
	return RuntimeStatusFailed
}

type queuedRuntime struct {
	runtime  *ManagedRuntime
	ctx      context.Context
	priority RequestPriority
	order    uint64
	canceled bool
	done     chan struct{}
	doneOnce sync.Once
}

func (q *queuedRuntime) deactivate() {
	q.doneOnce.Do(func() {
		close(q.done)
	})
}

type runtimeStartQueue []*queuedRuntime

func (q runtimeStartQueue) Len() int { return len(q) }

func (q runtimeStartQueue) Less(i, j int) bool {
	if q[i].priority == q[j].priority {
		return q[i].order < q[j].order
	}
	return q[i].priority > q[j].priority
}

func (q runtimeStartQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
}

func (q *runtimeStartQueue) Push(x any) {
	*q = append(*q, x.(*queuedRuntime))
}

func (q *runtimeStartQueue) Pop() any {
	old := *q
	n := len(old)
	entry := old[n-1]
	*q = old[:n-1]
	return entry
}

func rejectUnconfiguredPlan(req *Request, skills map[string]*skill.Skill) (*CoarsePlan, *RejectReason, error) {
	return nil, &RejectReason{
		Summary:  "task cannot proceed",
		Analysis: "no planning function is configured to map the request onto the registered skill set",
	}, nil
}
