// Package streamrender formats engine stream events for human-facing terminals.
//
// The package is a subscriber-side utility: it does not own streams, mutate
// events, or persist archives. CLI programs can attach a stream subscription,
// keep any JSONL/archive writer they already need, and pass the same events to
// a Renderer for compact status output.
package streamrender
