package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/tsumina/dango/internal/llm"
	runnerpkg "github.com/tsumina/dango/internal/orchestrate/runner"
)

var (
	defaultOrchestrator     *Orchestrator
	defaultOrchestratorOnce sync.Once
)

// Orchestrator is the singleton runner factory that bridges external user
// requests to runner assembly.
//
// It keeps a registry of lightweight skills loaded through llm.New,
// initializes its orchestrator-owned skill during startup, and materializes a
// fresh runner plus its Executor graph for each accepted plan.
type Orchestrator struct {
	logger            *slog.Logger
	orchestratorSkill *llm.Skill
	envClientOnce     sync.Once
	envClient         *llm.Client
	envClientErr      error

	mu                sync.RWMutex
	configLocked      bool
	runnerStore       runnerpkg.RunnerStore
	maxRunningRunners int
	skills            map[string]AddSkillConfig
	runners           map[string]*runnerpkg.Runner
	runningRunnerIDs  map[string]struct{}
	queuedRunnerByID  map[string]*queuedRunner
	queuedRunners     runnerStartQueue
	nextQueueOrder    uint64
}

func (o *Orchestrator) resolveEnvClient() (*llm.Client, error) {
	o.envClientOnce.Do(func() {
		o.envClient, o.envClientErr = llm.NewClientFromEnv()
	})
	return o.envClient, o.envClientErr
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
	o := &Orchestrator{
		logger:           logger,
		skills:           make(map[string]AddSkillConfig),
		runners:          make(map[string]*runnerpkg.Runner),
		runningRunnerIDs: make(map[string]struct{}),
		queuedRunnerByID: make(map[string]*queuedRunner),
	}
	sk, err := o.configuredOrchestratorSkill(defaultOrchestratorSkill())
	if err != nil {
		panic(fmt.Sprintf("orchestrate: initialize default orchestrator skill: %v", err))
	}
	o.orchestratorSkill = sk
	return o
}

func (o *Orchestrator) configuredOrchestratorSkill(sk *llm.Skill) (*llm.Skill, error) {
	if sk == nil {
		sk = defaultOrchestratorSkill()
	}
	if sk.Client() != nil && sk.Conversation() != nil {
		return sk, nil
	}
	client, err := o.resolveEnvClient()
	if err != nil || client == nil {
		return sk, nil
	}
	return bindOrchestratorSkill(sk, client)
}

// AddSkillConfig describes one lightweight skill plus the runtime wiring the
// runner will later pass to [llm.Skill.Bind].
//
// Skill must be a lightweight instance created by [llm.New]. AddSkills augments
// it with the built-in tools before placing it in the orchestrator registry.
// Client and Config are forwarded unchanged to the runner-owned bind step.
type AddSkillConfig struct {
	Skill  *llm.Skill
	Client *llm.Client
	Config *llm.ConversationConfig
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

// SetOrchestratorSkill replaces the current orchestrator-owned skill.
//
// Callers may provide either a lightweight skill or one that is already bound
// to an LLM client. Lightweight skills are eagerly bound to the orchestrator's
// env-derived client when one is available. Passing nil restores the embedded
// default skill. Like the other startup-only orchestrator settings, it must be
// called before the first planning call.
func (o *Orchestrator) SetOrchestratorSkill(sk *llm.Skill) error {
	configured, err := o.configuredOrchestratorSkill(sk)
	if err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.configLocked {
		return fmt.Errorf("orchestrate: SetOrchestratorSkill can only be called during startup")
	}
	if configured.Name == "" {
		return fmt.Errorf("orchestrate: orchestrator skill requires a non-empty name")
	}
	o.orchestratorSkill = configured
	return nil
}

// SetOrchestratorSkillDir replaces the built-in orchestrator skill with the
// lightweight skill loaded from dir.
//
// Passing an empty dir restores the embedded default skill. Like the other
// startup-only orchestrator settings, it must be called before the first
// planning call.
func (o *Orchestrator) SetOrchestratorSkillDir(dir string) error {
	if dir == "" {
		return o.SetOrchestratorSkill(nil)
	}
	sk, err := llm.New(dir, nil, nil)
	if err != nil {
		return err
	}
	builtinTools, err := sk.BuiltinTools()
	if err != nil {
		return err
	}
	sk, err = sk.WithTools(builtinTools...)
	if err != nil {
		return err
	}
	return o.SetOrchestratorSkill(sk)
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

// AddSkills appends one or more lightweight skills to the orchestrator's
// registry.
//
// Each config carries the unbound [llm.Skill] plus the client and conversation
// configuration that runner-owned execution will later pass into
// [llm.Skill.Bind]. AddSkills augments each skill with the built-in tools but
// does not bind it yet.
func (o *Orchestrator) AddSkills(cfgs ...AddSkillConfig) error {
	if len(cfgs) == 0 {
		return nil
	}
	prepared := make(map[string]AddSkillConfig, len(cfgs))
	for i, cfg := range cfgs {
		if cfg.Skill == nil {
			return fmt.Errorf("orchestrate: add skill config %d requires a non-nil skill", i)
		}
		if cfg.Skill.Conversation() != nil {
			return fmt.Errorf("orchestrate: add skill config %d requires a lightweight unbound skill", i)
		}
		builtinTools, err := cfg.Skill.BuiltinTools()
		if err != nil {
			return err
		}
		sk, err := cfg.Skill.WithTools(builtinTools...)
		if err != nil {
			return err
		}
		if sk.Name == "" {
			return fmt.Errorf("orchestrate: add skill config %d has empty skill name", i)
		}
		if _, exists := prepared[sk.Name]; exists {
			return fmt.Errorf("orchestrate: skill %q already provided in AddSkills", sk.Name)
		}
		prepared[sk.Name] = AddSkillConfig{
			Skill:  sk,
			Client: cfg.Client,
			Config: cloneConversationConfig(cfg.Config),
		}
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	for name := range prepared {
		if _, exists := o.skills[name]; exists {
			return fmt.Errorf("orchestrate: skill %q already registered", name)
		}
	}
	for name, cfg := range prepared {
		o.skills[name] = cfg
	}
	return nil
}

// RemoveSkills deletes one or more registered lightweight skills by name.
//
// Like AddSkills, RemoveSkills supports batch updates. The removal is atomic:
// every requested skill name must be valid and already registered before any
// entry is deleted. RemoveSkills remains allowed during the daemon lifetime so
// the available skill set can change while the Orchestrator is running.
func (o *Orchestrator) RemoveSkills(names ...string) error {
	if len(names) == 0 {
		return nil
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	seen := make(map[string]struct{}, len(names))
	for i, name := range names {
		if name == "" {
			return fmt.Errorf("orchestrate: remove skill name %d must be non-empty", i)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("orchestrate: skill %q already provided in RemoveSkills", name)
		}
		if _, exists := o.skills[name]; !exists {
			return fmt.Errorf("orchestrate: skill %q is not registered", name)
		}
		seen[name] = struct{}{}
	}
	for name := range seen {
		delete(o.skills, name)
	}
	return nil
}

// Skills returns a snapshot of the currently registered lightweight skill map.
func (o *Orchestrator) Skills() map[string]*llm.Skill {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return cloneSkillMap(o.skills)
}

func cloneConversationConfig(cfg *llm.ConversationConfig) *llm.ConversationConfig {
	if cfg == nil {
		return nil
	}
	clone := *cfg
	if cfg.AutoShrink != nil {
		auto := *cfg.AutoShrink
		clone.AutoShrink = &auto
	}
	return &clone
}

// OrchestratorSkill returns the startup-initialized skill reserved for
// orchestrator planning and review-style stages.
func (o *Orchestrator) OrchestratorSkill() *llm.Skill {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.orchestratorSkill
}

// Runners returns a snapshot of the runner records the Orchestrator has
// assembled so far.
func (o *Orchestrator) Runners() map[string]*runnerpkg.Runner {
	o.mu.RLock()
	defer o.mu.RUnlock()
	copyMap := make(map[string]*runnerpkg.Runner, len(o.runners))
	for id, runner := range o.runners {
		copyMap[id] = runner
	}
	return copyMap
}

// Runner returns the runner identified by id.
func (o *Orchestrator) Runner(id string) (*runnerpkg.Runner, error) {
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

// QueryRunner returns the outer-facing view of the runner identified by id.
func (o *Orchestrator) QueryRunner(id string) (*runnerpkg.RunnerView, error) {
	runner, err := o.Runner(id)
	if err != nil {
		return nil, err
	}
	return runner.View(), nil
}

// SubscribeRunner returns a stream of runner updates plus an unsubscribe
// function that stops further delivery.
func (o *Orchestrator) SubscribeRunner(id string, bufferSize int) (<-chan runnerpkg.RunnerUpdate, func(), error) {
	runner, err := o.Runner(id)
	if err != nil {
		return nil, nil, err
	}
	ch, unsubscribe := runner.SubscribeUpdates(bufferSize)
	return ch, unsubscribe, nil
}

// LoadRunnerRecords loads the persisted append-only record stream for the
// runner identified by id.
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

// RemoveRunner removes a runner from the Orchestrator and deletes its
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
	if !runnerpkg.IsRemovable(runner.State()) {
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
	if !runnerpkg.IsRemovable(current.State()) {
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

// StartRunner starts the runner identified by id and begins forwarding
// its status to query and stream consumers.
func (o *Orchestrator) StartRunner(ctx context.Context, id string) error {
	runner, err := o.Runner(id)
	if err != nil {
		return err
	}
	return o.startManagedRunner(ctx, runner, nil)
}

// watchRunnerDone releases the runner's execution slot when execution ends.
//
// A runner only consumes an execution slot while it is in
// [runner.PhaseExecuting]. Review and replan phases do not count against the
// concurrent execution limit.
func (o *Orchestrator) watchRunnerDone(runner *runnerpkg.Runner) {
	updates, unsubscribe := runner.SubscribeUpdates(16)
	defer unsubscribe()
	done := runner.Done()
	executing := false
	for {
		select {
		case <-done:
			if executing {
				o.releaseRunnerExecutionSlot(runner.ID())
			}
			return
		case update, ok := <-updates:
			if !ok {
				if executing {
					o.releaseRunnerExecutionSlot(runner.ID())
				}
				return
			}
			if update.Phase == runnerpkg.PhaseExecuting {
				executing = true
			}
			if executing && update.Event != nil && update.Event.Type == runnerpkg.EventEngineIdle {
				o.releaseRunnerExecutionSlot(runner.ID())
				executing = false
				return
			}
		}
	}
}
