package llm

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3/responses"
)

// StreamEvent is a single notification emitted by [Client.Stream].
//
// Only assistant output_text deltas are surfaced to consumers. All
// other model output (assistant message text, tool calls, reasoning
// items, token usage) is accumulated internally and committed to the
// bound [Conversation] when the stream terminates cleanly, matching
// the semantics of [Client.Send]. When the stream fails mid-flight,
// a single terminal event with Err set is emitted before the channel
// is closed and conv is left unchanged.
type StreamEvent struct {
	// TextDelta is a fragment of assistant output_text. It is empty
	// on a terminal error event.
	TextDelta string
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

// Stream issues one turn against the Responses API using conv's
// current state and returns a channel of [StreamEvent] carrying
// incremental output_text fragments as they arrive from the provider.
//
// The channel is closed by Stream's internal worker when the server
// closes the response stream. On clean completion, the full assistant
// response (text, tool calls, and when [ClientConfig.ReplayReasoning]
// is enabled, reasoning items) is appended to conv and token usage is
// recorded using the same rules as [Client.Send] so downstream
// consumers of the conversation do not have to care which transport
// was used. On mid-stream failure a terminal [StreamEvent] with Err
// set is sent before the channel is closed and conv is not mutated.
//
// Pre-stream errors (nil conv) are returned synchronously and the
// returned channel is nil. Callers that want to abort an in-flight
// stream should cancel ctx; the internal worker exits within a
// bounded time and will close the channel on its way out.
//
// The iteration pattern mirrors the openai-go responses-streaming
// example: the value returned by Responses.NewStreaming is driven
// with Next / Current / Err without naming its concrete type.
func (c *Client) Stream(ctx context.Context, conv *Conversation) (<-chan StreamEvent, error) {
	if conv == nil {
		return nil, fmt.Errorf("llm: Stream requires a non-nil conversation")
	}
	params := c.buildRequestParams(conv)
	stream := c.raw.Responses.NewStreaming(ctx, params)
	out := make(chan StreamEvent, streamBuffer)

	go func() {
		defer close(out)

		var completed *responses.Response
		for stream.Next() {
			evt := stream.Current()
			switch evt.Type {
			case "response.output_text.delta":
				if evt.Delta == "" {
					continue
				}
				select {
				case out <- StreamEvent{TextDelta: evt.Delta}:
				case <-ctx.Done():
					return
				}
			case "response.completed":
				r := evt.Response
				completed = &r
			case "response.failed", "response.incomplete":
				// Surface terminal failure events as an Err event so
				// the consumer does not treat a silently-closed
				// channel as success.
				select {
				case out <- StreamEvent{Err: fmt.Errorf("llm: stream %s", evt.Type)}:
				case <-ctx.Done():
				}
				return
			}
		}
		if err := stream.Err(); err != nil {
			select {
			case out <- StreamEvent{Err: err}:
			case <-ctx.Done():
			}
			return
		}
		if completed == nil {
			return
		}
		c.applyResponseOutput(ctx, conv, completed)
	}()

	return out, nil
}
