package llm

import (
	"context"
	"fmt"
)

// MaxSteps returns the iteration bound used by [Conversation.Run].
func (c *Conversation) MaxSteps() int { return c.maxSteps }

// SetMaxSteps overrides the iteration bound used by [Conversation.Run].
// Values less than or equal to zero are ignored so [DefaultMaxSteps]
// keeps its role as the sensible fallback.
func (c *Conversation) SetMaxSteps(n int) {
	if n > 0 {
		c.maxSteps = n
	}
}

// Run drives a single task to completion.
//
// Run appends userInput as a user turn, then repeatedly calls
// [Conversation.Send] and dispatches any function tool calls the model
// emits via the [Tool] instances registered at construction time until
// the model produces a final text response or the iteration budget set
// by [Conversation.SetMaxSteps] is exhausted. The returned string is
// the concatenated output_text of the final response.
//
// When the model requests a tool that was not registered, Run feeds an
// "unknown tool" error back to the model as the function_call_output so
// the loop can recover on the next turn rather than aborting. When a
// registered tool's Execute returns an error, its error message is
// similarly surfaced to the model as output.
//
// A session persistence failure observed during Run does not interrupt
// the loop; it is reported via the returned error once the model has
// produced a final response so the caller can still observe the reply.
//
// effort overrides the reasoning-effort level applied to every
// [Conversation.Send] this Run issues. Pass an empty string to fall
// back to the level configured on the bound [Client].
func (c *Conversation) Run(ctx context.Context, userInput string, effort ReasoningEffort) (string, error) {
	if c.client == nil {
		c.emitLLMFailure(ctx, ErrNoClient, "run")
		return "", ErrNoClient
	}
	budget := c.maxSteps
	if budget <= 0 {
		budget = DefaultMaxSteps
	}
	c.AppendUser(userInput)

	for step := 0; step < budget; step++ {
		resp, err := c.Send(ctx, effort)
		if err != nil {
			return "", fmt.Errorf("llm: run step %d: %w", step, err)
		}
		if len(resp.ToolCalls) == 0 {
			if lastErr := c.LastError(); lastErr != nil {
				runErr := fmt.Errorf("llm: session persistence failed: %w", lastErr)
				c.emitLLMFailure(ctx, runErr, "run")
				return resp.Text, runErr
			}
			return resp.Text, nil
		}
		for _, call := range resp.ToolCalls {
			c.emitToolExecutionStarted(ctx, call)
			output, execErr := c.dispatch(ctx, call)
			c.emitToolExecutionFinished(ctx, call, execErr)
			c.AppendToolOutput(call.CallID, output, execErr)
			// execErr is surfaced to the model via output so the loop
			// can recover on the next turn.
		}
	}
	err := fmt.Errorf("llm: run exceeded max steps (%d) without final response", budget)
	c.emitLLMFailure(ctx, err, "run")
	return "", err
}

// dispatch executes a single function call against the registered
// tools. On error the error text is returned as output so the model
// sees it; the caller (Run) appends the returned string as the
// function_call_output regardless of error.
func (c *Conversation) dispatch(ctx context.Context, call ToolCall) (string, error) {
	tool, ok := c.toolByName[call.Name]
	if !ok {
		msg := fmt.Sprintf("error: unknown tool %q", call.Name)
		return msg, fmt.Errorf("llm: unknown tool %q", call.Name)
	}
	out, err := tool.Execute(ctx, call.Arguments)
	if err != nil {
		return fmt.Sprintf("error: %s\n%s", err.Error(), out), err
	}
	return out, nil
}
