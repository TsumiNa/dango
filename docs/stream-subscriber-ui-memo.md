# Stream Subscriber UI Memo

Last updated: 2026-05-03

This memo tracks stream subscribers and user-facing debugging tools. Subscribers
consume the normalized request stream; they should not change producer behavior
or add side channels.

## Current Position

The first reusable terminal renderer lives in `internal/streamrender`.

It provides:

- compact one-line rendering for orchestrator, runner, agent, skill, tool,
  artifact, and LLM status events;
- configurable stream filtering and hidden event types;
- optional ANSI color;
- bounded text truncation;
- lightweight running-event spinner frames;
- artifact paths rendered as `file://` URLs;
- optional iTerm2-compatible inline image output for image artifacts;
- optional extraction of full exchange markdown string deltas into files linked
  from the terminal output.

The Honshu groundwater example uses this renderer while keeping its JSONL event
artifact log unchanged.

## JSONL And Archive Subscribers

JSONL artifact logging is intentionally simple: subscribe to the request stream
and encode each event as one JSON line. The Honshu example already does this.

Database/archive sinks are deferred. They should be designed with the stream
persistence deployment decision rather than added as part of terminal rendering.

## Debug UI Questions

A richer debug UI needs a separate design pass. Open questions:

- Should debug mode render untruncated event deltas, or expose full payloads only
  behind expansion controls?
- Should debug UI read from a live stream, a JSONL artifact, a database archive,
  or all three?
- Does it need debug-specific event types, or can it inspect the existing
  normalized event contract plus raw JSON payloads?
- How should exchange documents and referenced artifacts be browsed without
  leaking private skill input into orchestrator-facing views?

## Future PR Shape

- Improve terminal renderer ergonomics after real CLI use.
- Add snapshot tests for representative stream transcripts.
- Design a debug UI around archived JSONL first, before committing to database
  storage or new event families.