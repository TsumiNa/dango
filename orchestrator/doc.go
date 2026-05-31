// Package engine bridges external requests to runner-backed execution.
//
// The orchestrator owns request intake, planning, skill registration, runner
// creation, and external query/subscription APIs. Agents own individual
// skill runs and exchange markdown documents with the runner without exposing
// runner internals to skill prompts.
package orchestrator
