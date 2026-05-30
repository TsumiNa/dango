// Package mcpclient is a thin wrapper around the official
// github.com/modelcontextprotocol/go-sdk client.
//
// The package isolates the rest of dango from the SDK surface so individual
// call sites do not depend on the upstream SDK directly. It lives at
// internal/mcpclient (shared between internal/llm and internal/engine) so
// the llm package can expose its [llm.MCPTools] adapter and the engine
// package can register handles on the orchestrator without an extra
// re-export layer. [Server] is the live handle every caller holds; there
// is no further public wrapper in the llm package.
//
// A [Server] owns one live [mcp.ClientSession]. Construct one with [Start]
// (or [StartWithTransport] in tests); call [Server.Tools] to discover the
// server's tool catalogue and [Server.Call] to dispatch a single tool call.
// [Server.Close] shuts down the session, which closes stdin so a subprocess
// server can terminate cleanly via the SDK's pipeRWC.Close path.
//
// Result handling concatenates the [mcp.CallToolResult.Content] entries into
// a single string (text content verbatim; non-text content rendered as a
// short stub) and truncates the result at [ResultMaxBytes], mirroring the
// 16 KiB bash output cap.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ResultMaxBytes caps the assembled result string returned by [Server.Call].
// It mirrors the bash 16 KiB output cap so MCP results never flood the LLM
// context.
const ResultMaxBytes = 16 * 1024

// TruncationSuffix is appended when [Server.Call] truncates a result body.
const TruncationSuffix = "\n…truncated"

// dangoClientImpl identifies dango as the MCP client to remote servers.
var dangoClientImpl = &mcp.Implementation{Name: "dango", Version: "0.0.1"}

// ServerSpec describes one MCP server stdio process.
type ServerSpec struct {
	// Name is the user-chosen identifier the orchestrator and adapter use to
	// disambiguate tools from this server. It is also the namespacing prefix
	// for any tool the server exposes.
	Name string
	// Command is the executable to spawn.
	Command string
	// Args are the command-line arguments passed to Command.
	Args []string
	// Env is the environment passed to Command. When nil, the subprocess
	// inherits the parent process environment.
	Env []string
}

// ToolMetadata is the subset of [mcp.Tool] dango needs to surface a tool to
// the LLM.
type ToolMetadata struct {
	// Name is the bare tool name as reported by the server (no namespace
	// prefix).
	Name string
	// Description is the human-readable description the model sees.
	Description string
	// InputSchema is the JSON-schema parameter object forwarded to the LLM
	// verbatim via the existing llm.Tool.Parameters contract.
	InputSchema map[string]any
}

// Server is one live MCP client session, plus the captured tool catalogue.
//
// Server is safe for concurrent use across [Server.Call] invocations. The
// underlying SDK session serializes write traffic. [Server.Close] is
// idempotent.
type Server struct {
	spec    ServerSpec
	session *mcp.ClientSession
	tools   []ToolMetadata

	mu     sync.Mutex
	closed bool
}

// Start launches spec and connects an MCP client to it.
//
// When transport is nil, Start constructs an [exec.Cmd] from spec and wraps
// it in [mcp.CommandTransport]; spec.Command must be non-empty in that case,
// and spec.Args plus spec.Env are forwarded to the subprocess. When transport
// is non-nil, it is used directly and spec.Command / spec.Args / spec.Env are
// ignored — the test suites use this form to connect to an in-process
// transport. Either form waits for the MCP initialize handshake to complete
// and lists the server's tools before returning. The returned Server keeps
// the live session; the caller must call [Server.Close] when done.
func Start(ctx context.Context, spec ServerSpec, transport mcp.Transport) (*Server, error) {
	if spec.Name == "" {
		return nil, fmt.Errorf("mcpclient: server name must not be empty")
	}
	if transport == nil {
		if spec.Command == "" {
			return nil, fmt.Errorf("mcpclient: server %q has empty command", spec.Name)
		}
		cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
		if spec.Env != nil {
			cmd.Env = append([]string(nil), spec.Env...)
		}
		transport = &mcp.CommandTransport{Command: cmd}
	}

	client := mcp.NewClient(dangoClientImpl, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: connect %q: %w", spec.Name, err)
	}

	tools, err := listAllTools(ctx, session)
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("mcpclient: list tools for %q: %w", spec.Name, err)
	}

	return &Server{
		spec:    spec,
		session: session,
		tools:   tools,
	}, nil
}

func listAllTools(ctx context.Context, session *mcp.ClientSession) ([]ToolMetadata, error) {
	var out []ToolMetadata
	var cursor string
	for {
		res, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, t := range res.Tools {
			if t == nil {
				continue
			}
			out = append(out, ToolMetadata{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: coerceSchema(t.InputSchema),
			})
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

// coerceSchema normalizes the SDK's `any` schema into a map[string]any so it
// flows through llm.Tool.Parameters unchanged. The SDK reports the schema as
// the JSON unmarshal of the server's payload, which is already a
// map[string]any in practice; for safety, we round-trip through JSON when it
// is anything else.
func coerceSchema(schema any) map[string]any {
	if schema == nil {
		return objectSchema()
	}
	if m, ok := schema.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return objectSchema()
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil || out == nil {
		return objectSchema()
	}
	return out
}

func objectSchema() map[string]any {
	return map[string]any{"type": "object"}
}

// Name returns the configured server name.
func (s *Server) Name() string {
	if s == nil {
		return ""
	}
	return s.spec.Name
}

// Tools returns a snapshot of the server's tool catalogue captured at
// connect time.
//
// The returned slice is a fresh copy, so reordering or trimming it does not
// affect future calls. However, each entry's InputSchema map is shared with
// the Server's internal state — mutating that map will affect what later
// callers see. Treat the schema as read-only or call [maps.Clone] yourself
// before mutating. Tools the [llm.MCPTools] adapter produces are unaffected
// because the [llm.Tool.Parameters] contract deep-clones the schema on
// every call.
func (s *Server) Tools() []ToolMetadata {
	if s == nil {
		return nil
	}
	out := make([]ToolMetadata, len(s.tools))
	copy(out, s.tools)
	return out
}

// Call dispatches one tool call against the live MCP session.
//
// arguments is the raw JSON arguments string emitted by the model. An empty
// arguments string is sent as `{}`. The returned string is the concatenated
// content from the server, truncated at [ResultMaxBytes]. If the server
// reports IsError=true, Call returns the assembled string plus a non-nil
// error so the caller can surface it through the existing tool-error path.
func (s *Server) Call(ctx context.Context, name string, arguments string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("mcpclient: server not started")
	}
	if name == "" {
		return "", fmt.Errorf("mcpclient: tool name must not be empty")
	}
	session := s.activeSession()
	if session == nil {
		return "", fmt.Errorf("mcpclient: server not started")
	}

	var args any
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		args = map[string]any{}
	} else {
		if err := json.Unmarshal([]byte(arguments), &args); err != nil {
			return "", fmt.Errorf("mcpclient: arguments for %q are not valid JSON: %w", name, err)
		}
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("mcpclient: call tool %q: %w", name, err)
	}

	body := assembleResult(res)
	body = truncateResult(body)
	if res.IsError {
		msg := body
		if msg == "" {
			msg = "tool reported error"
		}
		return body, errors.New(msg)
	}
	return body, nil
}

// activeSession returns the live MCP session under the lock so concurrent
// [Server.Close] can swap it out without racing the read.
func (s *Server) activeSession() *mcp.ClientSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return s.session
}

// Close shuts down the underlying MCP session. It is safe to call multiple
// times. Subsequent [Server.Call] invocations return an error.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	session := s.session
	s.session = nil
	s.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close()
}

func assembleResult(res *mcp.CallToolResult) string {
	if res == nil || len(res.Content) == 0 {
		return ""
	}
	var b strings.Builder
	for i, c := range res.Content {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(renderContent(c))
	}
	return b.String()
}

func renderContent(c mcp.Content) string {
	switch v := c.(type) {
	case *mcp.TextContent:
		return v.Text
	case *mcp.ImageContent:
		return fmt.Sprintf("[image content, mime=%s, bytes=%d]", coalesce(v.MIMEType, "unknown"), len(v.Data))
	case *mcp.AudioContent:
		return fmt.Sprintf("[audio content, mime=%s, bytes=%d]", coalesce(v.MIMEType, "unknown"), len(v.Data))
	case *mcp.ResourceLink:
		return fmt.Sprintf("[resource link uri=%s name=%s]", v.URI, coalesce(v.Name, ""))
	case *mcp.EmbeddedResource:
		uri := ""
		if v.Resource != nil {
			uri = v.Resource.URI
		}
		return fmt.Sprintf("[embedded resource uri=%s]", uri)
	default:
		return fmt.Sprintf("[unknown content type %T]", c)
	}
}

func coalesce(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func truncateResult(s string) string {
	if len(s) <= ResultMaxBytes {
		return s
	}
	return s[:ResultMaxBytes] + TruncationSuffix
}
