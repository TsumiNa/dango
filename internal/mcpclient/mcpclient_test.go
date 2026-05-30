package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testTimeout = 5 * time.Second

func TestStartRejectsEmptyName(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if _, err := Start(ctx, ServerSpec{Command: "echo"}, nil); err == nil {
		t.Fatal("expected empty-name error")
	}
}

func TestStartRejectsEmptyCommand(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if _, err := Start(ctx, ServerSpec{Name: "demo"}, nil); err == nil {
		t.Fatal("expected empty-command error")
	}
}

func TestStartListsTools(t *testing.T) {
	t.Parallel()
	srv, ctx, cancel := setupServer(t)
	defer cancel()

	tools := srv.Tools()
	want := map[string]bool{"echo": false, "fail": false}
	for _, m := range tools {
		if _, ok := want[m.Name]; ok {
			want[m.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing tool %q in listing %+v", name, tools)
		}
	}
	_ = ctx
}

func TestCallReturnsConcatenatedText(t *testing.T) {
	t.Parallel()
	srv, ctx, cancel := setupServer(t)
	defer cancel()

	out, err := srv.Call(ctx, "echo", `{"text":"hello"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out != "hello" {
		t.Fatalf("unexpected output %q", out)
	}
}

func TestCallReturnsErrorWhenServerReportsIsError(t *testing.T) {
	t.Parallel()
	srv, ctx, cancel := setupServer(t)
	defer cancel()

	_, err := srv.Call(ctx, "fail", `{}`)
	if err == nil {
		t.Fatal("expected error from is-error tool")
	}
	if !strings.Contains(err.Error(), "bad arguments") {
		t.Fatalf("unexpected error %q", err)
	}
}

func TestCallTruncatesLargeResult(t *testing.T) {
	t.Parallel()
	srv, ctx, cancel := setupServer(t)
	defer cancel()

	out, err := srv.Call(ctx, "echo_n", `{"n":20000}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.HasSuffix(out, TruncationSuffix) {
		t.Fatalf("expected truncation suffix in output of length %d", len(out))
	}
	if len(out) > ResultMaxBytes+len(TruncationSuffix) {
		t.Fatalf("output not truncated to cap: %d", len(out))
	}
}

func TestCallRejectsMalformedArguments(t *testing.T) {
	t.Parallel()
	srv, ctx, cancel := setupServer(t)
	defer cancel()

	_, err := srv.Call(ctx, "echo", `{not json}`)
	if err == nil {
		t.Fatal("expected JSON parse error")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	srv, _, cancel := setupServer(t)
	defer cancel()

	if err := srv.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestRenderNonTextContent(t *testing.T) {
	t.Parallel()
	if got := renderContent(&mcp.ImageContent{MIMEType: "image/png", Data: []byte("abc")}); got != "[image content, mime=image/png, bytes=3]" {
		t.Fatalf("image: %q", got)
	}
	if got := renderContent(&mcp.AudioContent{MIMEType: "", Data: []byte("xx")}); got != "[audio content, mime=unknown, bytes=2]" {
		t.Fatalf("audio: %q", got)
	}
	if got := renderContent(&mcp.ResourceLink{URI: "file://a", Name: "n"}); got != "[resource link uri=file://a name=n]" {
		t.Fatalf("link: %q", got)
	}
	if got := renderContent(&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "file://b"}}); got != "[embedded resource uri=file://b]" {
		t.Fatalf("embedded: %q", got)
	}
}

func TestCoerceSchemaFallback(t *testing.T) {
	t.Parallel()
	if got := coerceSchema(nil)["type"]; got != "object" {
		t.Fatalf("nil schema fallback: %v", got)
	}
	raw := json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
	got := coerceSchema(raw)
	if got["type"] != "object" {
		t.Fatalf("raw schema lost type: %+v", got)
	}
}

// setupServer wires an in-memory MCP server with three deterministic tools so
// the Server-side tests do not need to spawn subprocesses.
func setupServer(t *testing.T) (*Server, context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "echoes back the supplied text",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		Text string `json:"text"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Text}}}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo_n",
		Description: "produces n bytes of output",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct {
		N int `json:"n"`
	}) (*mcp.CallToolResult, any, error) {
		if in.N < 0 {
			in.N = 0
		}
		text := strings.Repeat("a", in.N)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "fail",
		Description: "always returns IsError=true",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in any) (*mcp.CallToolResult, any, error) {
		res := &mcp.CallToolResult{}
		res.SetError(errors.New("bad arguments"))
		return res, nil, nil
	})

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		cancel()
		t.Fatalf("server connect: %v", err)
	}

	srv, err := Start(ctx, ServerSpec{Name: "test-server"}, clientTransport)
	if err != nil {
		cancel()
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, ctx, cancel
}
