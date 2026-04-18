package skill

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"github.com/tsumina/dango/internal/llm"
)

// DefaultMaxSteps bounds the number of tool-call/response iterations an
// [Agent] will perform before giving up. It protects against runaway loops
// when the model keeps requesting tool calls without producing a final
// message.
const DefaultMaxSteps = 20

// Agent drives a tool-using conversation with an LLM.
//
// The Agent wires together:
//
//   - a skill prompt and user input (turned into Responses API input),
//   - a set of [Tool] implementations advertised to the model,
//   - an execution loop that dispatches each requested tool call and feeds
//     the result back into the next request.
//
// Agents are cheap to construct and safe for concurrent use as long as the
// underlying [Tool] implementations are also safe for concurrent use. The
// zero value is not usable; construct one with [NewAgent].
type Agent struct {
	client     *llm.Client
	tools      []Tool
	toolByName map[string]Tool
	maxSteps   int
}

// AgentOption customizes [NewAgent].
type AgentOption func(*Agent)

// WithMaxSteps overrides the default iteration bound. Values less than or
// equal to zero are ignored.
func WithMaxSteps(n int) AgentOption {
	return func(a *Agent) {
		if n > 0 {
			a.maxSteps = n
		}
	}
}

// NewAgent creates an [Agent] bound to client and the given tools.
//
// client must be non-nil. Tool names must be unique; duplicates cause
// NewAgent to return an error so misconfigured tool sets fail fast rather
// than silently shadowing each other at call time.
func NewAgent(client *llm.Client, tools []Tool, opts ...AgentOption) (*Agent, error) {
	if client == nil {
		return nil, fmt.Errorf("skill: agent requires a non-nil client")
	}
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("skill: agent received nil tool")
		}
		name := t.Name()
		if name == "" {
			return nil, fmt.Errorf("skill: tool has empty name")
		}
		if _, dup := byName[name]; dup {
			return nil, fmt.Errorf("skill: duplicate tool name %q", name)
		}
		byName[name] = t
	}
	a := &Agent{
		client:     client,
		tools:      tools,
		toolByName: byName,
		maxSteps:   DefaultMaxSteps,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a, nil
}

// Tools returns the tools advertised by this agent in registration order.
func (a *Agent) Tools() []Tool { return a.tools }

// Run drives a single task to completion.
//
// instructions is inserted as the system-level instruction for every request
// in the loop (commonly the skill's Instruction body). userInput is the
// initial user message describing the task. Run issues a request, executes
// any function tool calls the model emits via the registered [Tool]
// instances, and feeds the results back until the model produces a final
// text response or the step budget is exhausted. The returned string is the
// concatenated output_text of the final response.
func (a *Agent) Run(ctx context.Context, instructions, userInput string) (string, error) {
	toolParams := a.toolParams()
	input := responses.ResponseInputParam{
		responses.ResponseInputItemParamOfMessage(userInput, responses.EasyInputMessageRoleUser),
	}

	for step := 0; step < a.maxSteps; step++ {
		params := responses.ResponseNewParams{
			Model: a.client.Model(),
			Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
			Tools: toolParams,
		}
		if instructions != "" {
			params.Instructions = openai.String(instructions)
		}
		resp, err := a.client.Raw().Responses.New(ctx, params)
		if err != nil {
			return "", fmt.Errorf("skill: agent request failed at step %d: %w", step, err)
		}

		calls := functionCalls(resp.Output)
		if len(calls) == 0 {
			return resp.OutputText(), nil
		}

		for _, call := range calls {
			callParam := call.ToParam()
			input = append(input, responses.ResponseInputItemUnionParam{OfFunctionCall: &callParam})
			output, execErr := a.dispatch(ctx, call)
			input = append(input, responses.ResponseInputItemParamOfFunctionCallOutput(call.CallID, output))
			_ = execErr // surfaced to the model via output; loop continues so it can recover
		}
	}

	return "", fmt.Errorf("skill: agent exceeded max steps (%d) without final response", a.maxSteps)
}

// dispatch executes a single function call against the registered tools.
// On error the error text is returned as output so the model sees it.
func (a *Agent) dispatch(ctx context.Context, call responses.ResponseFunctionToolCall) (string, error) {
	tool, ok := a.toolByName[call.Name]
	if !ok {
		msg := fmt.Sprintf("error: unknown tool %q", call.Name)
		return msg, fmt.Errorf("skill: unknown tool %q", call.Name)
	}
	out, err := tool.Execute(ctx, call.Arguments)
	if err != nil {
		msg := fmt.Sprintf("error: %s\n%s", err.Error(), out)
		return msg, err
	}
	return out, nil
}

// toolParams converts the registered Tools into the Responses API tool union.
func (a *Agent) toolParams() []responses.ToolUnionParam {
	out := make([]responses.ToolUnionParam, 0, len(a.tools))
	for _, t := range a.tools {
		ft := &responses.FunctionToolParam{
			Name:       t.Name(),
			Parameters: t.Parameters(),
		}
		if d := t.Description(); d != "" {
			ft.Description = openai.String(d)
		}
		out = append(out, responses.ToolUnionParam{OfFunction: ft})
	}
	return out
}

// functionCalls extracts the function_call items from a response's output.
func functionCalls(items []responses.ResponseOutputItemUnion) []responses.ResponseFunctionToolCall {
	var calls []responses.ResponseFunctionToolCall
	for _, item := range items {
		if item.Type == "function_call" {
			calls = append(calls, item.AsFunctionCall())
		}
	}
	return calls
}
