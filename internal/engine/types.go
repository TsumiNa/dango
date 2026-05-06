package engine

import runnerpkg "github.com/tsumina/dango/internal/engine/runner"

// CoarsePlan is the orchestrator's high-level task graph before execution
// starts. It is defined in the runner package and re-exported here so
// orchestrator callers can refer to planning results without importing the
// runner package directly.
type CoarsePlan = runnerpkg.CoarsePlan

// CoarsePlanNode describes one executor-sized unit in a [CoarsePlan].
type CoarsePlanNode = runnerpkg.CoarsePlanNode

// PlanReview is the planner-owned review decision for a polished plan.
type PlanReview = runnerpkg.PlanReview
