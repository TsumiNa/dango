package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tsumina/dango/internal/llm/toolpolicy"
)

// Approver decides whether a need_approve tool call may proceed.
//
// Approver lives next to dispatch because need_approve gating is consulted
// from inside [Conversation.Run]'s tool-call loop; the interface and its
// adapter sit here so the dispatch site and the contract that drives it
// stay readable together.
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error)
}

// ApproverFunc adapts a plain function into an [Approver].
type ApproverFunc func(context.Context, ApprovalRequest) (ApprovalResponse, error)

// Approve implements [Approver].
func (f ApproverFunc) Approve(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error) {
	return f(ctx, req)
}

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
		resp, err := c.runStep(ctx, effort)
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
			output, execErr, decision := c.dispatch(ctx, call)
			c.emitToolExecutionFinished(ctx, call, execErr, decision)
			c.AppendToolOutput(call.CallID, output, execErr)
			// execErr is surfaced to the model via output so the loop
			// can recover on the next turn.
		}
	}
	err := fmt.Errorf("llm: run exceeded max steps (%d) without final response", budget)
	c.emitLLMFailure(ctx, err, "run")
	return "", err
}

func (c *Conversation) runStep(ctx context.Context, effort ReasoningEffort) (*Response, error) {
	if c.eventStream == nil {
		return c.Send(ctx, effort)
	}
	return c.streamResponse(ctx, effort, nil, true)
}

// dispatch executes a single function call against the registered
// tools. On error the error text is returned as output so the model
// sees it; the caller (Run) appends the returned string as the
// function_call_output regardless of error.
func (c *Conversation) dispatch(ctx context.Context, call ToolCall) (string, error, Decision) {
	tool, ok := c.toolByName[call.Name]
	if !ok {
		msg := fmt.Sprintf("error: unknown tool %q", call.Name)
		return msg, fmt.Errorf("llm: unknown tool %q", call.Name), Decision{}
	}
	decision := toolpolicy.Decision{}
	argsSummary, _ := compactJSONText(call.Arguments)
	execCtx := toolpolicy.WithRecorder(ctx, &decision)
	execCtx = toolpolicy.WithCallMetadata(execCtx, call.CallID, call.Name, argsSummary)
	execCtx = toolpolicy.WithApprover(execCtx, c.requestApproval)
	out, err := tool.Execute(execCtx, call.Arguments)
	if err != nil {
		return fmt.Sprintf("error: %s\n%s", err.Error(), out), err, decision
	}
	return out, nil, decision
}

func (c *Conversation) requestApproval(ctx context.Context, req toolpolicy.ApprovalRequest) (toolpolicy.ApprovalResponse, error) {
	c.emitToolApprovalRequested(ctx, req)
	if c.approver == nil {
		resp := toolpolicy.ApprovalResponse{
			Outcome: toolpolicy.ApprovalOutcomeDeny,
			Reason:  "no approver configured",
		}
		err := &toolpolicy.ApprovalDeniedError{Capability: req.Capability, Reason: resp.Reason}
		c.emitToolApprovalResolved(ctx, req, resp, err)
		return resp, err
	}

	approvalCtx := ctx
	cancel := func() {}
	if c.approvalTimeout > 0 {
		approvalCtx, cancel = context.WithTimeout(ctx, c.approvalTimeout)
	}
	defer cancel()

	resp, err := c.approver.Approve(approvalCtx, ApprovalRequest(req))
	if err != nil {
		if c.approvalTimeout > 0 && errors.Is(approvalCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			resp = toolpolicy.ApprovalResponse{
				Outcome: toolpolicy.ApprovalOutcomeDeny,
				Reason:  fmt.Sprintf("approval timed out after %s", c.approvalTimeout.Round(time.Millisecond)),
			}
			err = &toolpolicy.ApprovalDeniedError{Capability: req.Capability, Reason: resp.Reason}
		}
		c.emitToolApprovalResolved(ctx, req, resp, err)
		return resp, err
	}
	switch resp.Outcome {
	case toolpolicy.ApprovalOutcomeApprove, toolpolicy.ApprovalOutcomeApproveForSession:
		c.emitToolApprovalResolved(ctx, req, resp, nil)
		return resp, nil
	case "", toolpolicy.ApprovalOutcomeDeny:
		if resp.Outcome == "" {
			resp.Outcome = toolpolicy.ApprovalOutcomeDeny
		}
		if resp.Reason == "" {
			resp.Reason = "approval denied"
		}
		err = &toolpolicy.ApprovalDeniedError{Capability: req.Capability, Reason: resp.Reason}
		c.emitToolApprovalResolved(ctx, req, resp, err)
		return resp, err
	default:
		err = fmt.Errorf("llm: invalid approval outcome %q", resp.Outcome)
		c.emitToolApprovalResolved(ctx, req, resp, err)
		return resp, err
	}
}
