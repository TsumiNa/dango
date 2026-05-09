package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	persistencepkg "github.com/tsumina/dango/internal/engine/runner/persistence"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
)

// Orchestrator is a runner factory that bridges external user requests to
// runner assembly.
//
// It keeps a registry of lightweight skills loaded through llm.New,
// initializes its orchestrator-owned skill during startup, and materializes a
// fresh runner plus its Executor graph for each accepted plan.
type Orchestrator struct {
	ctx               context.Context
	logger            *slog.Logger
	orchestratorSkill *llm.Skill
	envClientOnce     sync.Once
	envClient         *llm.Client
	envClientErr      error

	mu                      sync.RWMutex
	configLocked            bool
	persistence             persistencepkg.Backend
	runnerPathRule          persistencepkg.PathRule
	promptTemplateOverrides map[string]string
	maxRunningRunners       int
	skills                  map[string]SkillRegistration
	runners                 map[string]*runnerpkg.Runner
	runningRunnerIDs        map[string]struct{}
	queuedRunnerByID        map[string]*queuedRunner
	queuedRunners           runnerStartQueue
	nextQueueOrder          uint64
}

// OrchestratorOption adjusts a constructed [Orchestrator] before it is returned.
type OrchestratorOption func(*Orchestrator)

// WithOrchestratorContext installs ctx as the Orchestrator's base lifecycle
// context.
//
// The Orchestrator keeps a reference to ctx and observes its cancellation when
// deriving operation contexts. A nil context is ignored. The caller remains
// responsible for canceling ctx when the surrounding service should stop.
func WithOrchestratorContext(ctx context.Context) OrchestratorOption {
	return func(o *Orchestrator) {
		if ctx != nil {
			o.ctx = ctx
		}
	}
}

// WithOrchestratorLogger installs logger as the Orchestrator's lifecycle
// logger.
//
// The Orchestrator keeps a reference to logger. slog.Logger values are safe for
// concurrent use; callers that wrap a handler with additional mutable state are
// responsible for that handler's synchronization.
func WithOrchestratorLogger(logger *slog.Logger) OrchestratorOption {
	return func(o *Orchestrator) {
		if logger != nil {
			o.logger = logger
		}
	}
}

// WithPersistence installs backend as the orchestrator-owned persistence
// backend used for runner records, request event logs, snapshot cursors, and
// runner workspace provisioning.
func WithPersistence(backend persistencepkg.Backend) OrchestratorOption {
	return func(o *Orchestrator) {
		o.persistence = backend
	}
}

// WithRunnerPathRule installs rule as the per-runner workspace path mapper.
//
// Nil keeps [persistence.DefaultPathRule].
func WithRunnerPathRule(rule persistencepkg.PathRule) OrchestratorOption {
	return func(o *Orchestrator) {
		if rule != nil {
			o.runnerPathRule = rule
		}
	}
}

// WithPromptTemplateOverrides installs advanced executor prompt template
// overrides for runners created by this Orchestrator.
//
// The Orchestrator keeps its own copy of overrides and forwards it to each
// runner. Template names must match the built-in executor prompt template names
// such as "polish.tmpl", "execute.tmpl", or "report.tmpl".
func WithPromptTemplateOverrides(overrides map[string]string) OrchestratorOption {
	return func(o *Orchestrator) {
		o.promptTemplateOverrides = cloneStringMap(overrides)
	}
}

// ErrRunnerNotFound is returned when an Orchestrator runner lookup misses.
var ErrRunnerNotFound = errors.New("orchestrate: runner not found")

// ErrRunnerActive is returned when callers attempt to remove a runner that
// is still live and may continue to accept work.
var ErrRunnerActive = errors.New("orchestrate: runner is still active")

// ErrRunnerStoreNotConfigured is returned when persisted runner records are
// requested without a configured persistence backend runner store.
var ErrRunnerStoreNotConfigured = errors.New("orchestrate: runner store not configured")

// ErrEventLogStoreNotConfigured is returned when request-level describe replay
// is requested without a configured persistence backend event-log store.
var ErrEventLogStoreNotConfigured = errors.New("orchestrate: event log store not configured")

// ErrRunnerPlanNotAwaitingReview is returned when callers try to accept or
// reject a plan while the runner is not waiting for review.
var ErrRunnerPlanNotAwaitingReview = errors.New("orchestrate: runner plan is not awaiting review")

// ErrRunnerPlanNotAwaitingReplan is returned when callers try to provide a
// replacement plan while the runner is not waiting for replan.
var ErrRunnerPlanNotAwaitingReplan = errors.New("orchestrate: runner plan is not awaiting replan")

// ErrRunnerNotExecuting is returned when callers try to complete a runner
// that is not currently executing.
var ErrRunnerNotExecuting = errors.New("orchestrate: runner is not executing")

// ErrRunnerExecutionSlotsFull is returned when a reviewed runner is ready to
// execute but no execution slot is currently available.
var ErrRunnerExecutionSlotsFull = errors.New("orchestrate: no execution slots available")

func (o *Orchestrator) resolveEnvClient() (*llm.Client, error) {
	o.envClientOnce.Do(func() {
		o.envClient, o.envClientErr = llm.NewClientFromEnv()
	})
	return o.envClient, o.envClientErr
}

// SetClient configures the default LLM client used by the embedded
// orchestrator skill and skill directories registered with
// [Orchestrator.AddSkillDirs].
//
// It is intended for tests, examples, and applications that already constructed
// a client and want the orchestrator to own the remaining binding work. It must
// be called before the first planning call. The Orchestrator may share client
// across concurrent runner work; [llm.Client] is safe for concurrent request
// use, but callers must not mutate it or its raw SDK client while work is in
// flight.
func (o *Orchestrator) SetClient(client *llm.Client) error {
	if client == nil {
		return fmt.Errorf("orchestrate: SetClient requires a non-nil client")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.configLocked {
		return fmt.Errorf("orchestrate: SetClient can only be called during startup")
	}
	o.envClientOnce.Do(func() {})
	o.envClient = client
	o.envClientErr = nil
	skill, err := bindOrchestratorSkill(o.orchestratorSkill, client)
	if err != nil {
		return err
	}
	o.orchestratorSkill = skill
	return nil
}

// NewOrchestrator constructs a new Orchestrator.
//
// The returned Orchestrator is independent and does not share global mutable
// state with other instances. Options can install startup dependencies such as
// a service context, logger, or persistence stores.
func NewOrchestrator(opts ...OrchestratorOption) *Orchestrator {
	o := &Orchestrator{
		ctx:              context.Background(),
		logger:           slog.Default(),
		runnerPathRule:   persistencepkg.DefaultPathRule,
		skills:           make(map[string]SkillRegistration),
		runners:          make(map[string]*runnerpkg.Runner),
		runningRunnerIDs: make(map[string]struct{}),
		queuedRunnerByID: make(map[string]*queuedRunner),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	embedded, err := loadOrchestratorSkill(orchestratorRegistryTools(o)...)
	if err != nil {
		panic(fmt.Sprintf("orchestrate: initialize default orchestrator skill: %v", err))
	}
	sk, err := o.configuredOrchestratorSkill(embedded)
	if err != nil {
		panic(fmt.Sprintf("orchestrate: initialize default orchestrator skill: %v", err))
	}
	o.orchestratorSkill = sk
	return o
}

func (o *Orchestrator) operationContext(ctx context.Context) context.Context {
	base := o.ctx
	if base == nil {
		base = context.Background()
	}
	if ctx == nil {
		return base
	}
	return ctxWithValues(base, ctx)
}

func ctxWithValues(parent context.Context, child context.Context) context.Context {
	if child == nil {
		return parent
	}
	if merged, ok := child.(*mergedContext); ok && merged.parent == parent {
		return child
	}
	return &mergedContext{parent: parent, child: child}
}

type mergedContext struct {
	parent context.Context
	child  context.Context
	once   sync.Once
	done   chan struct{}
	errMu  sync.Mutex
	err    error
}

func (c *mergedContext) Deadline() (time.Time, bool) {
	parentDeadline, parentOK := c.parent.Deadline()
	childDeadline, childOK := c.child.Deadline()
	switch {
	case parentOK && childOK:
		if childDeadline.Before(parentDeadline) {
			return childDeadline, true
		}
		return parentDeadline, true
	case childOK:
		return childDeadline, true
	case parentOK:
		return parentDeadline, true
	default:
		return time.Time{}, false
	}
}

func (c *mergedContext) Done() <-chan struct{} {
	parentDone := c.parent.Done()
	childDone := c.child.Done()
	if parentDone == nil {
		return childDone
	}
	if childDone == nil {
		return parentDone
	}
	c.once.Do(func() {
		c.done = make(chan struct{})
		go func() {
			select {
			case <-parentDone:
				c.setErr(c.parent.Err())
			case <-childDone:
				c.setErr(c.child.Err())
			}
			close(c.done)
		}()
	})
	return c.done
}

func (c *mergedContext) Err() error {
	if err := c.cachedErr(); err != nil {
		return err
	}
	select {
	case <-c.parent.Done():
		return c.setErr(c.parent.Err())
	default:
	}
	select {
	case <-c.child.Done():
		return c.setErr(c.child.Err())
	default:
	}
	return nil
}

func (c *mergedContext) cachedErr() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

func (c *mergedContext) setErr(err error) error {
	if err == nil {
		return nil
	}
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if c.err == nil {
		c.err = err
	}
	return c.err
}

func (c *mergedContext) Value(key any) any {
	if value := c.child.Value(key); value != nil {
		return value
	}
	return c.parent.Value(key)
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

// SkillRegistration describes one lightweight skill plus the runtime wiring the
// runner will later pass to [llm.Skill.Bind].
//
// Skill must be a lightweight instance created by [llm.NewSkill]. AddSkills
// augments it with the built-in command and filesystem tools before placing it
// in the orchestrator registry. AccessibleDirs is applied while those built-in
// tools are rebuilt, so runtime skills can inspect upstream resources, write
// glue code in their playground, and run commands through the runner-owned
// bind step. Client and Config are forwarded unchanged to that bind step.
type SkillRegistration struct {
	Skill          *llm.Skill
	AccessibleDirs []string
	Client         *llm.Client
	Config         llm.ConversationConfig
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
	sk, err := llm.NewSkill(dir, llm.DefaultSkillConfig())
	if err != nil {
		return err
	}
	sk, err = sk.SetAccessibleDirsAndBuiltinTools()
	if err != nil {
		return err
	}
	return o.SetOrchestratorSkill(sk)
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
func (o *Orchestrator) AddSkills(cfgs ...SkillRegistration) error {
	if len(cfgs) == 0 {
		return nil
	}
	prepared := make(map[string]SkillRegistration, len(cfgs))
	for i, cfg := range cfgs {
		if cfg.Skill == nil {
			return fmt.Errorf("orchestrate: add skill config %d requires a non-nil skill", i)
		}
		if cfg.Skill.Conversation() != nil {
			return fmt.Errorf("orchestrate: add skill config %d requires a lightweight unbound skill", i)
		}
		sk, err := cfg.Skill.SetAccessibleDirsAndBuiltinTools(cfg.AccessibleDirs...)
		if err != nil {
			return err
		}
		if sk.Name == "" {
			return fmt.Errorf("orchestrate: add skill config %d has empty skill name", i)
		}
		if _, exists := prepared[sk.Name]; exists {
			return fmt.Errorf("orchestrate: skill %q already provided in AddSkills", sk.Name)
		}
		prepared[sk.Name] = SkillRegistration{
			Skill:          sk,
			AccessibleDirs: append([]string(nil), cfg.AccessibleDirs...),
			Client:         cfg.Client,
			Config:         cloneConversationConfig(cfg.Config),
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

// AddSkillDirs loads one or more skill directories and registers them with the
// orchestrator.
//
// When [Orchestrator.SetClient] or the environment has configured a default
// client, that client is used for these skill runtimes. Otherwise each skill is
// left to resolve its client from its own environment when the runner binds it.
func (o *Orchestrator) AddSkillDirs(cfg llm.ConversationConfig, dirs ...string) error {
	if len(dirs) == 0 {
		return nil
	}
	client, _ := o.resolveEnvClient()
	cfgs := make([]SkillRegistration, 0, len(dirs))
	for i, dir := range dirs {
		if dir == "" {
			return fmt.Errorf("orchestrate: add skill dir %d must not be empty", i)
		}
		sk, err := llm.NewSkill(dir, llm.DefaultSkillConfig())
		if err != nil {
			return err
		}
		cfgs = append(cfgs, SkillRegistration{
			Skill:  sk,
			Client: client,
			Config: cfg,
		})
	}
	return o.AddSkills(cfgs...)
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

func cloneConversationConfig(cfg llm.ConversationConfig) llm.ConversationConfig {
	clone := cfg
	if cfg.AutoShrink != nil {
		auto := *cfg.AutoShrink
		clone.AutoShrink = &auto
	}
	if cfg.StreamMetadata != nil {
		clone.StreamMetadata = make(map[string]any, len(cfg.StreamMetadata))
		for k, v := range cfg.StreamMetadata {
			clone.StreamMetadata[k] = v
		}
	}
	return clone
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

// WaitRunner blocks until the runner identified by id settles or ctx is
// canceled, then returns the latest query view.
//
// The returned view reflects the runner state at the time WaitRunner returns,
// even when ctx cancellation caused the wait to end before the runner settled.
func (o *Orchestrator) WaitRunner(ctx context.Context, id string) (*runnerpkg.RunnerView, error) {
	ctx = o.operationContext(ctx)
	runner, err := o.Runner(id)
	if err != nil {
		return nil, err
	}
	waitErr := runner.Wait(ctx)
	return runner.View(), waitErr
}

// SubscribeRunnerStream subscribes to the compact structured event stream for
// runner id. Late subscribers receive replay according to the stream
// subscription options.
func (o *Orchestrator) SubscribeRunnerStream(id string, filter streampkg.Filter, opts ...streampkg.SubscribeOption) (*streampkg.Subscription, error) {
	runner, err := o.Runner(id)
	if err != nil {
		return nil, err
	}
	return runner.SubscribeStream(filter, opts...)
}

// LoadRunnerRecords loads the persisted append-only record stream for the
// runner identified by id.
func (o *Orchestrator) LoadRunnerRecords(ctx context.Context, id string) ([]runnerpkg.RunnerRecord, error) {
	ctx = o.operationContext(ctx)
	if id == "" {
		return nil, fmt.Errorf("orchestrate: runner id must not be empty")
	}

	o.mu.RLock()
	backend := o.persistence
	o.mu.RUnlock()
	var store runnerpkg.RunnerStore
	if backend != nil {
		store = backend.RunnerStore()
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
	ctx = o.operationContext(ctx)
	if id == "" {
		return fmt.Errorf("orchestrate: runner id must not be empty")
	}

	o.mu.RLock()
	runner := o.runners[id]
	backend := o.persistence
	o.mu.RUnlock()
	var store runnerpkg.RunnerStore
	if backend != nil {
		store = backend.RunnerStore()
	}
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
	ctx = o.operationContext(ctx)
	runner, err := o.Runner(id)
	if err != nil {
		return err
	}
	return o.startManagedRunner(ctx, runner)
}

// watchRunnerDone releases the runner's execution slot when its managed
// lifecycle settles.
func (o *Orchestrator) watchRunnerDone(runner *runnerpkg.Runner) {
	<-runner.Done()
	o.releaseRunnerExecutionSlot(runner.ID())
}
