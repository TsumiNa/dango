// Package spec defines the canonical machine contracts used across dango.
//
// The package contains the data structures that make the registry, planner,
// runner, executor, and persistence layers agree on what a tool, a task plan,
// an executor refinement, or a handoff means. Core entry points include
// [ToolSpec] and [MergeToolSpec] for registry configuration, [DAGPlan] and
// [PlannedEdge] for runner planning, [ExecutorPlan] for executor-side detail
// refinement, [Handoff] and [RenderHandoff] or [ParseHandoff] for edge
// completion artifacts, and [NewUUID] for stable task and edge identifiers.
//
// The usual workflow through these types spans multiple packages. The
// orchestrator registers a tool by validating and merging a [ToolSpec]. The
// runner constructs a [DAGPlan] composed of [PlannedEdge] values and asks an
// executor to fill in the edge detail as an [ExecutorPlan]. During execution,
// the executor writes a [Handoff], and the runner parses that handoff to update
// task and edge state and to produce the final task result. Because these types
// are shared at every stage, they are the package to read first when tracing a
// request from registration to completion.
//
// The package intentionally stays close to pure data modeling and validation.
// It should encode schema, normalization, serialization, and merge behavior,
// while higher-level workflow packages decide when those contracts are created,
// persisted, reviewed, executed, or finalized.
package spec
