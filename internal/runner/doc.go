// Package runner contains task execution services that operate independently
// from the orchestrator control plane.
//
// The package owns task runner lifecycle management, background execution,
// edge scheduling, and executor dispatch. Orchestrator code should start,
// inspect, or control runners through this package rather than keeping
// execution mechanics in the control plane.
package runner
