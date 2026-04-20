package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/tsumina/dango/internal/llm/skill"
	runnerpkg "github.com/tsumina/dango/internal/orchestrate/runner"
)

var (
	defaultOrchestrator     *Orchestrator
	defaultOrchestratorOnce sync.Once
)

// Orchestrator is the singleton runner factory that bridges external user
// requests to runner assembly.
//
// It keeps a registry of lightweight skills loaded through skill.Load, asks a
// planner to convert a request into a coarse execution plan, and materializes a
// fresh runner plus its Executor graph for each accepted plan.
type Orchestrator struct {
	logger *slog.Logger

	mu                sync.RWMutex
	configLocked      bool
	runnerStore       runnerpkg.RunnerStore
	maxRunningRunners int
	skills            map[string]*skill.Skill
	runners           map[string]*ManagedRunner
	runningRunnerIDs  map[string]struct{}
	queuedRunnerByID  map[string]*queuedRunner
	queuedRunners     runnerStartQueue
	nextQueueOrder    uint64
	planFn            PlanningFunc
}

// Default returns the process-wide Orchestrator singleton.
//
// The singleton is always initialized with slog.Default. Callers that need a
// different logger can update it afterwards through Orchestrator.SetLogger.
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
		logger:           logger,
		skills:           make(map[string]*skill.Skill),
		runners:          make(map[string]*ManagedRunner),
		runningRunnerIDs: make(map[string]struct{}),
		queuedRunnerByID: make(map[string]*queuedRunner),
		planFn:           rejectUnconfiguredPlan,
	}
}

// SetLogger replaces the Orchestrator logger.
//
// Passing nil restores slog.Default so the Orchestrator and any runner it
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

// SetRunnerStore configures the persistence store that newly assembled
// runners should use.
//
// Passing nil clears any previously configured store. Like the other
// Orchestrator configuration entry points, it can only be called during
// startup.
func (o *Orchestrator) SetRunnerStore(store runnerpkg.RunnerStore) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.configLocked {
		return fmt.Errorf("orchestrate: SetRunnerStore can only be called during startup")
	}
	o.runnerStore = store
	return nil
}

// SetMaxRunningRunners configures how many runners may execute at once for
// StartRequest submissions.
//
// A limit of zero disables throttling and starts every submitted request
// immediately. The limit is startup-only like the other orchestrator-wide
// execution settings.
func (o *Orchestrator) SetMaxRunningRunners(limit int) error {
	if limit < 0 {
		return fmt.Errorf("orchestrate: max running runners must be non-negative")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.configLocked {
		return fmt.Errorf("orchestrate: SetMaxRunningRunners can only be called during startup")
	}
	o.maxRunningRunners = limit
	return nil
}

// RegisterSkill loads dir through skill.Load and stores the resulting
// lightweight Skill in the registry under its declared name.
//
// Skills are live managed state rather than startup-only configuration, so
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
// Like RegisterSkill, RemoveSkill is allowed during the daemon lifetime so the
// available skill set can change while the Orchestrator is running.
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

// Runners returns a snapshot of the runner records the Orchestrator has
// assembled so far.
func (o *Orchestrator) Runners() map[string]*ManagedRunner {
	o.mu.RLock()
	defer o.mu.RUnlock()
	copyMap := make(map[string]*ManagedRunner, len(o.runners))
	for id, runner := range o.runners {
		copyMap[id] = runner
	}
	return copyMap
}

// Runner returns the managed runner identified by id.
func (o *Orchestrator) Runner(id string) (*ManagedRunner, error) {
	if id == "" {
		return nil, fmt.Errorf("orchestrate: runner id must not be empty")
	}

	o.mu.RLock()
	defer o.mu.RUnlock()
	runner := o.runners[id]
	if runner == nil {
		return nil, ErrRunnerNotFound
	}
	return runner, nil
}

// QueryRunner returns the outer-facing view of the managed runner identified
// by id.
func (o *Orchestrator) QueryRunner(id string) (*RunnerView, error) {
	runner, err := o.Runner(id)
	if err != nil {
		return nil, err
	}
	return runner.view(), nil
}

// SubscribeRunner returns a stream of runner updates plus an unsubscribe
// function that stops further delivery.
func (o *Orchestrator) SubscribeRunner(id string, bufferSize int) (<-chan RunnerUpdate, func(), error) {
	runner, err := o.Runner(id)
	if err != nil {
		return nil, nil, err
	}
	ch, unsubscribe := runner.subscribe(bufferSize)
	return ch, unsubscribe, nil
}

// LoadRunnerRecords loads the persisted append-only record stream for the
// managed runner identified by id.
func (o *Orchestrator) LoadRunnerRecords(ctx context.Context, id string) ([]runnerpkg.RunnerRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return nil, fmt.Errorf("orchestrate: runner id must not be empty")
	}

	o.mu.RLock()
	store := o.runnerStore
	_, ok := o.runners[id]
	o.mu.RUnlock()
	if !ok {
		return nil, ErrRunnerNotFound
	}
	if store == nil {
		return nil, ErrRunnerStoreNotConfigured
	}
	return store.Load(ctx, id)
}

// RemoveRunner removes a managed runner from the Orchestrator and deletes its
// persisted log when one exists.
//
// Only pending, failed, or canceled runners may be removed. Running or idle
// runners are still live and therefore rejected.
func (o *Orchestrator) RemoveRunner(ctx context.Context, id string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return fmt.Errorf("orchestrate: runner id must not be empty")
	}

	o.mu.RLock()
	runner := o.runners[id]
	store := o.runnerStore
	o.mu.RUnlock()
	if runner == nil {
		return ErrRunnerNotFound
	}
	if !runnerRemovable(runner.Runner.State()) {
		return ErrRunnerActive
	}
	if store != nil {
		if err := store.Delete(ctx, id); err != nil && !errors.Is(err, runnerpkg.ErrRunnerLogNotFound) {
			return err
		}
	}

	o.mu.Lock()
	current := o.runners[id]
	if current == nil {
		o.mu.Unlock()
		return ErrRunnerNotFound
	}
	if !runnerRemovable(current.Runner.State()) {
		o.mu.Unlock()
		return ErrRunnerActive
	}
	if entry := o.queuedRunnerByID[id]; entry != nil {
		entry.canceled = true
		delete(o.queuedRunnerByID, id)
		entry.deactivate()
	}
	delete(o.runners, id)
	toStart, toCancel := o.collectQueuedStartsLocked()
	o.mu.Unlock()
	o.finishQueuedDispatch(toStart, toCancel)
	return nil
}

// StartRunner starts the managed runner identified by id and begins forwarding
// its status to query and stream consumers.
func (o *Orchestrator) StartRunner(ctx context.Context, id string) error {
	runner, err := o.Runner(id)
	if err != nil {
		return err
	}
	return o.startManagedRunner(ctx, runner)
}
