// Package taskflow contains the shared request, metadata, and task view
// contracts used between the orchestrator and runner packages.
//
// The package exists so that request normalization and task inspection do not
// need to be redefined in every layer that touches a task. [RequestEnvelope],
// [RequestPart], and [RequestMetadata] describe what entered the system.
// [TaskMetadata], [TaskLineage], and [TaskEvent] describe the append-only
// metadata that accompanies a persisted task. [TaskSummary], [TaskDescription],
// and [TaskRunResult] describe the list, detail, and terminal views that move
// across package boundaries.
//
// The normal workflow starts in the orchestrator, where HTTP or CLI input is
// converted into a [RequestEnvelope] and [RequestMetadata], normalized with
// [NormalizeRequestEnvelope], and persisted as part of [TaskMetadata]. The
// runner then consumes the same metadata to plan and execute a task, appending
// [TaskEvent] values as lifecycle state changes. When callers inspect a task,
// they receive either a [TaskSummary], a [TaskDescription], or a
// [TaskRunResult], depending on whether they need a list view, a full persisted
// view, or the terminal result of a synchronous run.
//
// This package therefore models the cross-package language of task requests and
// task history. It should remain free of control-plane or execution-plane
// policy, leaving those responsibilities to orchestrator and runner while
// preserving one stable contract between them.
package taskflow
