package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsumina/dango/internal/ai"
	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/runner"
	"github.com/tsumina/dango/internal/store/sqlite"
	"github.com/tsumina/dango/internal/taskflow"
)

// staticIntentLLMClient is a test LLM client that returns a pre-baked JSON payload.
type staticIntentLLMClient struct {
	payload []byte
	err     error
}

func (c staticIntentLLMClient) CompleteJSON(_ context.Context, _ ai.Request) ([]byte, string, error) {
	if c.err != nil {
		return nil, "", c.err
	}
	return append([]byte(nil), c.payload...), "", nil
}

func newServerTestFixture(t *testing.T) (*Server, *runner.TaskRunnerService) {
	t.Helper()

	root := t.TempDir()
	locator, err := datadir.New(filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("datadir.New() error = %v", err)
	}
	if err := locator.Ensure(); err != nil {
		t.Fatalf("locator.Ensure() error = %v", err)
	}

	store, err := sqlite.Open(locator.DBPath())
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	taskService := NewTaskService(locator, store, nil)
	runners := runner.NewTaskRunnerService(locator, taskService, nil, nil, nil)
	// nil client = passthrough (no intent understanding).
	return NewServer(ServerConfig{}, nil, taskService, runners, nil, nil), runners
}

func TestRemoveUnixSocketRemovesStaleSocket(t *testing.T) {
	t.Parallel()

	tempFile, err := os.CreateTemp("/tmp", "dango-sock-*.sock")
	if err != nil {
		t.Fatalf("os.CreateTemp() error = %v", err)
	}
	socketPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		t.Fatalf("tempFile.Close() error = %v", err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatalf("os.Remove(%q) error = %v", socketPath, err)
	}
	t.Cleanup(func() {
		_ = os.Remove(socketPath)
	})

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix) error = %v", err)
	}
	defer listener.Close()

	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("os.Stat(%q) before cleanup error = %v", socketPath, err)
	}
	if err := removeUnixSocket(socketPath); err != nil {
		t.Fatalf("removeUnixSocket() error = %v", err)
	}
	if _, err := os.Stat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("os.Stat(%q) after cleanup error = %v, want not exists", socketPath, err)
	}
}

func TestRemoveUnixSocketRejectsNonSocketPath(t *testing.T) {
	t.Parallel()

	filePath := filepath.Join(t.TempDir(), "orchestrator.sock")
	if err := os.WriteFile(filePath, []byte("not a socket"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	err := removeUnixSocket(filePath)
	if err == nil {
		t.Fatal("removeUnixSocket() error = nil, want non-socket error")
	}
	if _, statErr := os.Stat(filePath); statErr != nil {
		t.Fatalf("os.Stat(%q) error = %v, want file to remain", filePath, statErr)
	}
}

func TestHandleRequestTaskListIntentReturnsTasks(t *testing.T) {
	t.Parallel()

	server, runners := newServerTestFixture(t)

	for _, prompt := range []string{"first task", "second task"} {
		if _, err := runners.Create(context.Background(), taskflow.RequestEnvelope{Text: prompt}); err != nil {
			t.Fatalf("Create(%q) error = %v", prompt, err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/v0/request", strings.NewReader(`{"intent":"task/list"}`))
	recorder := httptest.NewRecorder()

	server.handleRequest(recorder, req)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status code = %d, want %d; body = %s", got, want, recorder.Body.String())
	}

	var response struct {
		Tasks []taskflow.TaskSummary `json:"tasks"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response error = %v", err)
	}
	if got, want := len(response.Tasks), 2; got != want {
		t.Fatalf("len(response.Tasks) = %d, want %d", got, want)
	}
	requests := map[string]bool{}
	for _, item := range response.Tasks {
		requests[item.Task.Request] = true
	}
	for _, want := range []string{"first task", "second task"} {
		if !requests[want] {
			t.Fatalf("task list missing request %q in %#v", want, response.Tasks)
		}
	}
}

func TestHandleTasksPostCreatesTask(t *testing.T) {
	t.Parallel()

	server, _ := newServerTestFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/v0/tasks", strings.NewReader(`{"text":"task from collection endpoint"}`))
	recorder := httptest.NewRecorder()

	server.handleTasks(recorder, req)

	if got, want := recorder.Code, http.StatusCreated; got != want {
		t.Fatalf("status code = %d, want %d; body = %s", got, want, recorder.Body.String())
	}

	var response taskflow.TaskDescription
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response error = %v", err)
	}
	if got, want := response.Task.Request, "task from collection endpoint"; got != want {
		t.Fatalf("response.Task.Request = %q, want %q", got, want)
	}
}

func TestHandleTaskRunsPostNormalizesRequestWithIntentHook(t *testing.T) {
	t.Parallel()

	server, _ := newServerTestFixture(t)

	// Build the intent result JSON that the mock client will return.
	result := intentResult{
		Request: taskflow.RequestEnvelope{Text: "normalized request", Meta: map[string]string{"intent": "write"}},
		Summary: "normalized by test",
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(intentResult) error = %v", err)
	}
	server.llmClient = staticIntentLLMClient{payload: payload}

	req := httptest.NewRequest(http.MethodPost, "/v0/task-runs", strings.NewReader(`{"text":"raw request","meta":{"source":"http"}}`))
	recorder := httptest.NewRecorder()

	server.handleTaskRuns(recorder, req)

	if got, want := recorder.Code, http.StatusAccepted; got != want {
		t.Fatalf("status code = %d, want %d; body = %s", got, want, recorder.Body.String())
	}

	var response taskflow.TaskDescription
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response error = %v", err)
	}
	if got, want := response.Task.Request, "normalized request"; got != want {
		t.Fatalf("response.Task.Request = %q, want %q", got, want)
	}
	if got, want := response.Metadata.Request.Meta["source"], "http"; got != want {
		t.Fatalf("response.Metadata.Request.Meta[source] = %q, want %q", got, want)
	}
	if got, want := response.Metadata.Request.Meta["intent"], "write"; got != want {
		t.Fatalf("response.Metadata.Request.Meta[intent] = %q, want %q", got, want)
	}
	if got, want := response.Metadata.Request.Meta["intent_summary"], "normalized by test"; got != want {
		t.Fatalf("response.Metadata.Request.Meta[intent_summary] = %q, want %q", got, want)
	}
}

func TestHandleTaskByIDDescribeActionReturnsTask(t *testing.T) {
	t.Parallel()

	server, runners := newServerTestFixture(t)
	created, err := runners.Create(context.Background(), taskflow.RequestEnvelope{Text: "task for describe action"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	path := fmt.Sprintf("/v0/tasks/%s/describe", created.Task.ID)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	recorder := httptest.NewRecorder()

	server.handleTaskByID(recorder, req)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status code = %d, want %d; body = %s", got, want, recorder.Body.String())
	}

	var response taskflow.TaskDescription
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response error = %v", err)
	}
	if got, want := response.Task.ID, created.Task.ID; got != want {
		t.Fatalf("response.Task.ID = %q, want %q", got, want)
	}
}
