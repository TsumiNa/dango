// Package main provides the process entrypoint for the dango binary.
//
// The executable exposes two primary modes:
//   - orchestrator mode for registration, planning, scheduling, and serving
//   - executor mode for in-tool describe and run entrypoints
//
// Most reusable behavior lives under internal packages. Package main is limited
// to bootstrapping process lifecycle and delegating command dispatch.
package main
