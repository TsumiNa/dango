package runner

import (
	"context"
	"log/slog"

	"github.com/tsumina/dango/internal/llm"
)

// PersistenceHandle exposes runner persistence sinks and workspace root.
//
// Implementations are typically orchestrator-owned and shared across runners.
// Runners keep a reference to the handle and may call its methods for their
// entire lifecycle. Callers must ensure the handle remains valid and that
// returned objects stay usable while a runner may still use them, and must
// provide synchronization when sharing mutable implementations.
type PersistenceHandle interface {
	// RunnerStore returns the append/load store used for runner lifecycle records.
	RunnerStore() RunnerStore
	// WorkspaceRoot returns the global workspace root. Runners combine it with a
	// path rule to provision their own per-runner workspace directory.
	WorkspaceRoot() string
}

// Option adjusts a constructed [Runner] before it is returned.
type Option func(*Runner)

// WithContext installs ctx as the Runner's base lifecycle context.
//
// The Runner keeps a reference to ctx and observes its cancellation during
// runner-owned work. A nil context is ignored. The caller remains responsible
// for canceling the context when the surrounding operation should stop.
func WithContext(ctx context.Context) Option {
	return func(r *Runner) {
		if ctx != nil {
			r.ctx = ctx
		}
	}
}

// WithLogger installs logger as the Runner's lifecycle logger.
//
// The Runner keeps a reference to logger. slog.Logger values are safe for
// concurrent use; callers that wrap a handler with additional mutable state are
// responsible for that handler's synchronization.
func WithLogger(logger *slog.Logger) Option {
	return func(r *Runner) {
		if logger != nil {
			r.logger = logger
		}
	}
}

// WithPersistenceHandle installs handle as the Runner's persistence source.
//
// The Runner keeps a reference to handle and resolves its runner store and
// workspace root during construction. A nil handle disables persistence and
// workspace provisioning.
func WithPersistenceHandle(handle PersistenceHandle) Option {
	return func(r *Runner) {
		r.persistenceHandle = handle
		if handle == nil {
			r.store = nil
			r.workspaceRoot = ""
			return
		}
		r.store = handle.RunnerStore()
		r.workspaceRoot = handle.WorkspaceRoot()
	}
}

// WithTrustedResourceRoots installs additional trusted roots for handoff
// artifact filtering and tool access.
//
// Existing directories are canonicalized and kept on the runner. Non-existent
// or invalid paths are ignored. The runner stores canonical string paths, not
// live handles; callers may manage the source roots independently, but changing
// filesystem contents after construction can change what tools can read/write.
// These roots are combined with the workspace root from [PersistenceHandle]
// when determining executor-accessible directories.
func WithTrustedResourceRoots(roots ...string) Option {
	return func(r *Runner) {
		canonicalRoots := make([]string, 0, len(roots))
		for _, root := range roots {
			if canonical, ok := canonicalExistingDir(root); ok && !containsDir(canonicalRoots, canonical) {
				canonicalRoots = append(canonicalRoots, canonical)
			}
		}
		r.trustedResourceRoots = canonicalRoots
	}
}

// WithRootPathRule installs rule as the mapping from runner ID to per-runner
// workspace subdirectory under the global workspace root.
func WithRootPathRule(rule func(string) string) Option {
	return func(r *Runner) {
		if rule != nil {
			r.rootPathRule = rule
		}
	}
}

// WithInitialPlan installs the initial coarse plan and materialized node graph.
//
// The Runner clones plan and nodes before storing them, so callers may mutate
// their originals after construction without changing the Runner.
func WithInitialPlan(plan *CoarsePlan, nodes map[string]*Node) Option {
	return func(r *Runner) {
		r.plan = CloneCoarsePlan(plan)
		r.initialNodes = cloneNodeMap(nodes)
	}
}

// WithPlannerSkill installs the skill used for plan review and replanning.
//
// The Runner keeps a reference to skill and binds runtime copies from it during
// lifecycle work. Callers must not mutate the lightweight skill concurrently
// with a running Runner unless they provide their own synchronization.
func WithPlannerSkill(skill *llm.Skill) Option {
	return func(r *Runner) {
		r.plannerSkill = skill
	}
}

// WithSkillSummaries installs the skill summaries available to replanning.
//
// The Runner copies the slice before storing it.
func WithSkillSummaries(summaries []SkillSummary) Option {
	return func(r *Runner) {
		r.skillSummaries = append([]SkillSummary(nil), summaries...)
	}
}

// WithPlanNodeBuilder installs builder as the callback used to materialize a
// node graph after replanning.
//
// The Runner keeps a reference to builder and calls it from runner lifecycle
// work. Callers own any state captured by the callback and are responsible for
// synchronization if that state is shared concurrently.
func WithPlanNodeBuilder(builder PlanNodeBuilder) Option {
	return func(r *Runner) {
		r.planNodeBuilder = builder
	}
}
