package runner

import (
	"context"
	"log/slog"

	"github.com/tsumina/dango/internal/llm"
)

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

// WithStore installs store as the Runner's persistence sink.
//
// The Runner keeps a reference to store and may call it from runner lifecycle
// goroutines. A nil store disables persistence. If store is shared with other
// goroutines, callers are responsible for synchronization unless the
// RunnerStore implementation documents its own concurrency safety.
func WithStore(store RunnerStore) Option {
	return func(r *Runner) {
		r.store = store
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
