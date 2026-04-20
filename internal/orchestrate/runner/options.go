package runner

import "log/slog"

// Option configures a [Runner] at construction time.
//
// Options are applied by [New] in order. They are the only way to set
// startup-only fields (logger, store, plan) so a Runner becomes immutable
// with respect to those fields once constructed.
type Option func(*Runner)

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

// WithPlan associates a [CoarsePlan] and its materialized node graph with
// the runner. When set, [Runner.Start] auto-adds the provided nodes to the
// execution engine and [Runner.View] surfaces the plan to observers.
//
// The nodes map is keyed by node ID and is expected to contain every node
// referenced by plan; runner does not validate the mapping.
func WithPlan(plan *CoarsePlan, nodes map[string]*Node) Option {
	return func(r *Runner) {
		r.plan = CloneCoarsePlan(plan)
		r.initialNodes = nodes
	}
}
