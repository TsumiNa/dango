// Package stream provides a small structured event bus for cross-layer output.
//
// Streams are scoped to one logical request/run/session and deliver JSON-safe
// event chunks to any number of subscribers. Producers emit compact deltas;
// consumers decide whether to render, persist, replay, or ignore those events.
package stream
