package runner

import (
	"context"
	"log/slog"

	"github.com/tsumina/dango/internal/llm"
)

// Setup groups the startup inputs used to construct a Runner.
//
// Setup is intentionally not a configuration object: fields such as Store,
// PlannerSkill, and PlanNodeBuilder are live dependencies or callbacks. The
// zero value is valid and creates a bare runner with default logger, background
// context, no persistence, and no initial plan.
type Setup struct {
	// Context is the base context used by runner-owned lifecycle work when
	// callers do not pass a more specific context. A nil Context uses
	// context.Background.
	Context context.Context

	// Logger receives engine messages. A nil Logger uses slog.Default.
	Logger *slog.Logger

	// Store receives append-only runner persistence records. A nil Store disables
	// persistence.
	Store RunnerStore

	// Plan is the initial coarse plan owned by the runner. NewWithSetup clones it
	// before storing it.
	Plan *CoarsePlan

	// Nodes is the materialized graph for Plan, keyed by node ID. NewWithSetup
	// clones the map before storing it.
	Nodes map[string]*Node

	// PlannerSkill reviews polished plans and produces replans after the initial
	// plan has been created.
	PlannerSkill *llm.Skill

	// SkillSummaries are the skills available to replanning. NewWithSetup copies
	// the slice before storing it.
	SkillSummaries []SkillSummary

	// PlanNodeBuilder materializes a fresh node graph when replanning produces a
	// revised plan.
	PlanNodeBuilder PlanNodeBuilder
}
