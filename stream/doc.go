// Package stream provides a structured communication primitive for engine
// layers.
//
// A Stream is the engine's featureful channel abstraction: it preserves the
// simple producer/subscriber synchronization shape of channels while adding
// scoped metadata, filtering, replay, fan-out, merge, optional persistence, and
// structured JSON-safe event payloads. Orchestrator, runner, and bound skill
// runtimes use streams as their communication surface instead of blocking on
// each other through call stacks; agents and nodes add scheduling context to
// skill streams before merging them upward.
package stream
