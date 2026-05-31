package llm

// ToolSpec is the minimal description of a function tool that is advertised
// to the model. It intentionally stays in the [llm] package so callers do
// not depend on the OpenAI SDK's parameter types.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolCall is a single function call requested by the model.
type ToolCall struct {
	CallID    string
	Name      string
	Arguments string
}

// ToolCallPayload holds the data stored on a tool_call or tool_output
// [Turn]. CallID pairs a call with its output so [Conversation.Trim] and
// [Conversation.DropToolDetails] can keep them in sync.
type ToolCallPayload struct {
	CallID    string
	Name      string
	Arguments string
	Output    string
	Error     string
}
