package llm

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3/responses"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

// StreamCategory is a bitmask selecting which kinds of incremental
// fragments [Conversation.Stream] forwards to its consumer. Categories
// compose with bitwise OR.
type StreamCategory uint

const (
	// StreamText forwards assistant output_text deltas.
	StreamText StreamCategory = 1 << iota
	// StreamReasoning forwards reasoning_text and
	// reasoning_summary_text deltas so UIs can show the model
	// thinking before the answer starts streaming.
	StreamReasoning
)

// DefaultStreamCategories is the category set used when
// [ClientConfig.StreamCategories] is left at its zero value.
const DefaultStreamCategories = StreamText | StreamReasoning

// Has reports whether s contains all of the bits in flag.
func (s StreamCategory) Has(flag StreamCategory) bool {
	return s&flag == flag
}

// resolveStreamCategories returns the effective category set for a
// freshly constructed [Client]. The zero value is treated as
// [DefaultStreamCategories]; any non-zero value is honored verbatim
// (including unrelated future bits, which are simply ignored by the
// stream worker).
func resolveStreamCategories(s StreamCategory) StreamCategory {
	if s == 0 {
		return DefaultStreamCategories
	}
	return s
}

// StreamEvent is a single notification emitted by [Conversation.Stream].
//
// Two kinds of progress deltas are surfaced to consumers: assistant
// output_text fragments (via [StreamEvent.TextDelta]) and reasoning
// fragments (via [StreamEvent.ReasoningDelta]). Reasoning deltas are
// exposed so UIs can show the model "thinking" during the long gap
// that often precedes the first visible answer token. The set of
// categories actually forwarded is configured by
// [ClientConfig.StreamCategories]; categories that are not selected
// are silently dropped from the channel.
//
// Exactly one of TextDelta / ReasoningDelta / Err is set on any
// given event. All other model output (message aggregation, tool
// calls, stored reasoning items, token usage) is accumulated
// internally and committed to the bound [Conversation] when the
// stream terminates cleanly, matching the semantics of
// [Conversation.Send]. When the stream fails mid-flight, a single
// terminal event with Err set is emitted before the channel is
// closed and the conversation is left unchanged.
type StreamEvent struct {
	// TextDelta is a fragment of assistant output_text.
	TextDelta string
	// ReasoningDelta is a fragment of the model's reasoning stream.
	// It aggregates both reasoning_text.delta and
	// reasoning_summary_text.delta provider events; consumers that
	// only care about one or the other should not rely on the
	// distinction being preserved here.
	ReasoningDelta string
	// Err, when non-nil, marks the final event on the channel and
	// reports the reason the stream did not complete successfully.
	// No further events will be sent after an Err event; the channel
	// is closed immediately after.
	Err error
}

// streamBuffer bounds how far the producer can run ahead of a slow
// consumer. The value is intentionally modest so a stuck consumer
// applies backpressure on the provider rather than buffering large
// responses in memory.
const streamBuffer = 16

// Stream issues one turn against the Responses API using the
// conversation's current state and returns a channel of [StreamEvent]
// carrying incremental output_text fragments as they arrive from the
// provider.
//
// The channel is closed by Stream's internal worker when the server
// closes the response stream. On clean completion, the full assistant
// response (text, tool calls, and when [ClientConfig.ReplayReasoning]
// is enabled, reasoning items) is appended to the conversation and
// token usage is recorded using the same rules as
// [Conversation.Send] so downstream consumers of the conversation do
// not have to care which transport was used. On mid-stream failure a
// terminal [StreamEvent] with Err set is sent before the channel is
// closed and the conversation is not mutated.
//
// Pre-stream errors ([ErrNoClient] when the conversation has no bound
// [Client]) are returned synchronously and the returned channel is
// nil. Callers that want to abort an in-flight stream should cancel
// ctx; the internal worker exits within a bounded time and will close
// the channel on its way out.
//
// Concurrency: Stream takes exclusive ownership of the conversation
// for the duration of the stream. [Conversation] is not safe for
// concurrent use, and the worker goroutine appends the assistant
// reply on clean completion, so the caller must not read or mutate
// the conversation from any other goroutine until the returned
// channel is closed (i.e., until the consuming range loop exits).
// After the channel closes, the conversation is safe to use again
// from the caller's goroutine.
//
// Terminal guarantee: when the channel closes, exactly one of the
// following has happened: (a) the server emitted response.completed
// and the conversation has been updated with the full reply; (b) a
// [StreamEvent] with Err was emitted and the conversation is
// unchanged; or (c) ctx was cancelled, in which case no Err event is
// required and ctx.Err() is the authoritative reason. No other
// "silent close" case exists: a stream that ends without either
// response.completed or a transport error surfaces a synthetic Err
// so consumers never mistake an interrupted stream for success.
//
// The iteration pattern mirrors the openai-go responses-streaming
// example: the value returned by Responses.NewStreaming is driven
// with Next / Current / Err without naming its concrete type.
//
// effort overrides the reasoning-effort level for this request only;
// pass an empty string to use the level configured on the bound
// [Client].
func (c *Conversation) Stream(ctx context.Context, effort ReasoningEffort) (<-chan StreamEvent, error) {
	if c.client == nil {
		return nil, ErrNoClient
	}
	out := make(chan StreamEvent, streamBuffer)

	go func() {
		defer close(out)
		_, err := c.streamResponse(ctx, effort, func(event StreamEvent) bool {
			select {
			case out <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}, false)
		if err != nil && ctx.Err() == nil {
			select {
			case out <- StreamEvent{Err: err}:
			case <-ctx.Done():
			}
		}
	}()

	return out, nil
}

func (c *Conversation) streamResponse(ctx context.Context, effort ReasoningEffort, forward func(StreamEvent) bool, emitFinalText bool) (*Response, error) {
	if c.client == nil {
		return nil, ErrNoClient
	}
	params := c.buildRequestParams(effort)
	stream := c.client.raw.Responses.NewStreaming(ctx, params)
	categories := resolveStreamCategories(c.client.streamCategories)

	var completed *responses.Response
	for stream.Next() {
		evt := stream.Current()
		switch evt.Type {
		case "response.output_text.delta":
			if evt.Delta == "" || !categories.Has(StreamText) {
				continue
			}
			c.emitTextDelta(ctx, streampkg.EventLLMOutputDelta, streampkg.StatusRunning, evt.Delta)
			if forward != nil && !forward(StreamEvent{TextDelta: evt.Delta}) {
				return nil, ctx.Err()
			}
		case "response.reasoning_text.delta",
			"response.reasoning_summary_text.delta":
			if evt.Delta == "" || !categories.Has(StreamReasoning) {
				continue
			}
			c.emitTextDelta(ctx, streampkg.EventLLMReasoningDelta, streampkg.StatusRunning, evt.Delta)
			if forward != nil && !forward(StreamEvent{ReasoningDelta: evt.Delta}) {
				return nil, ctx.Err()
			}
		case "response.completed":
			r := evt.Response
			completed = &r
		case "response.failed", "response.incomplete":
			err := fmt.Errorf("llm: stream %s", evt.Type)
			c.emitStreamEvent(ctx, streampkg.EventStatusFailed, streampkg.StatusFailed, err.Error(), nil)
			return nil, err
		}
	}
	if err := stream.Err(); err != nil {
		c.emitStreamEvent(ctx, streampkg.EventStatusFailed, streampkg.StatusFailed, err.Error(), nil)
		return nil, err
	}
	if completed == nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		err := fmt.Errorf("llm: stream ended without response.completed")
		c.emitStreamEvent(ctx, streampkg.EventStatusFailed, streampkg.StatusFailed, err.Error(), nil)
		return nil, err
	}
	resp := c.applyResponseOutput(ctx, completed, false)
	if emitFinalText && resp.Text != "" {
		c.emitTextDelta(ctx, streampkg.EventLLMOutputDelta, streampkg.StatusCompleted, resp.Text)
	}
	return resp, nil
}
