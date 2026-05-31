// Package runner provides the execution engine, markdown exchange envelope,
// and append-only persistence primitives used by the orchestrate package.
//
// Runner owns graph execution and lifecycle observation. Agents remain the
// unit of work, while exchange documents provide the human-readable data plane
// passed between polish, execution, report, planner review, and downstream
// skills.
package runner
