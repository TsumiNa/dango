package runner

import (
	"context"
	"log/slog"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"github.com/tsumina/dango/internal/llm"
)

// Option configures a [Runner] at construction time.
//
// Options are applied by [New] in order. They are the only way to set
// startup-only fields (logger, store, plan) so a Runner becomes immutable
// with respect to those fields once constructed.
type Option func(*Runner)

// WithContext sets the base context used by runner-owned lifecycle work when
// callers do not pass a more specific context.
func WithContext(ctx context.Context) Option {
	return func(r *Runner) {
		if ctx == nil {
			ctx = context.Background()
		}
		r.ctx = ctx
	}
}

// WithLogger sets the logger the runner emits engine messages through.
// Passing nil restores [slog.Default].
func WithLogger(logger *slog.Logger) Option {
	return func(r *Runner) {
		if logger == nil {
			logger = slog.Default()
		}
		r.logger = logger
	}
}

// WithStore attaches an append-only persistence store to the runner.
// Passing nil leaves persistence disabled.
func WithStore(store RunnerStore) Option {
	return func(r *Runner) {
		r.store = store
	}
}

// WithStream attaches a request-scoped output stream for compact lifecycle
// events. Passing nil leaves stream emission disabled.
func WithStream(eventStream *streampkg.Stream) Option {
	return func(r *Runner) {
		r.eventStream = eventStream
	}
}

// WithPlan associates a [CoarsePlan] and its materialized node graph with
// the runner. When set, [Runner.Start] auto-adds the provided nodes to the
// execution engine and [Runner.View] surfaces the plan to observers.
//
// The nodes map is keyed by node ID and is expected to contain every node
// referenced by plan; runner does not validate the mapping.
func WithPlan(plan *CoarsePlan, nodes map[string]*Node) Option {
	return func(r *Runner) {
		r.plan = CloneCoarsePlan(plan)
		r.initialNodes = cloneNodeMap(nodes)
	}
}

// WithPlannerSkill sets the skill the runner uses for review and replan after
// the initial plan has been created.
func WithPlannerSkill(sk *llm.Skill) Option {
	return func(r *Runner) {
		r.plannerSkill = sk
	}
}

// WithSkillSummaries records the skills available to replanning.
func WithSkillSummaries(summaries []SkillSummary) Option {
	return func(r *Runner) {
		r.skillSummaries = append([]SkillSummary(nil), summaries...)
	}
}

// WithPlanNodeBuilder sets the materializer used when the runner replans and
// needs a fresh node graph for the revised plan.
func WithPlanNodeBuilder(builder PlanNodeBuilder) Option {
	return func(r *Runner) {
		r.planNodeBuilder = builder
	}
}
