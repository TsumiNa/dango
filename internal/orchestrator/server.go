package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/runner"
	"github.com/tsumina/dango/internal/taskflow"
)

// ServerConfig controls which listeners the orchestrator API exposes.
//
// Empty addresses disable the corresponding listener. The same [Server]
// instance may serve both a TCP listener and a Unix domain socket listener at
// the same time so local automation and remote clients can share one control
// plane process.
type ServerConfig struct {
	// TCPAddress is the TCP listen address for the public HTTP API.
	TCPAddress string
	// UnixSocketPath is the filesystem path for the Unix domain socket listener.
	UnixSocketPath string
}

// Server hosts the orchestrator HTTP API for registry access and task control.
//
// A Server is the control-plane entrypoint that translates HTTP requests into
// registry mutations, task persistence operations, request normalization, and
// runner control calls. It depends on RegistryService for tool administration,
// TaskService for durable task state, TaskRunnerService for execution control,
// and an optional LLM client for intent-understanding. The zero value is not
// usable; callers construct it with [NewServer].
type Server struct {
	config    ServerConfig
	registry  *RegistryService
	tasks     *TaskService
	runners   *runner.TaskRunnerService
	llmClient llm.Client
	logger    *slog.Logger
}

type serverListener struct {
	entrypoint string
	listener   net.Listener
	server     *http.Server
}

type requestPayload struct {
	Intent      string                 `json:"intent,omitempty"`
	TaskID      string                 `json:"task_id,omitempty"`
	Text        string                 `json:"text,omitempty"`
	Parts       []taskflow.RequestPart `json:"parts,omitempty"`
	Meta        map[string]string      `json:"meta,omitempty"`
	AutoRun     bool                   `json:"auto_run,omitempty"`
	CloneReason string                 `json:"clone_reason,omitempty"`
}

type requestIntentHandler func(*Server, http.ResponseWriter, *http.Request, requestPayload)
type serverMethodHandler func(*Server, http.ResponseWriter, *http.Request)
type taskActionHandler func(*Server, http.ResponseWriter, *http.Request, string)

type taskActionRoute struct {
	method  string
	handler taskActionHandler
}

const serverReadHeaderTimeout = 5 * time.Second

var requestIntentHandlers = map[string]requestIntentHandler{
	"task/list":     (*Server).handleRequestTaskListIntent,
	"task/describe": (*Server).handleRequestTaskDescribeIntent,
	"task/cancel":   (*Server).handleRequestTaskCancelIntent,
	"task/resume":   (*Server).handleRequestTaskResumeIntent,
	"task/clone":    (*Server).handleRequestTaskCloneIntent,
}

var taskCollectionHandlers = map[string]serverMethodHandler{
	http.MethodGet:  (*Server).handleTasksListMethod,
	http.MethodPost: (*Server).handleTasksCreateMethod,
}

var taskActionHandlers = map[string]taskActionRoute{
	"":         {method: http.MethodGet, handler: (*Server).handleTaskDescribeAction},
	"describe": {method: http.MethodGet, handler: (*Server).handleTaskDescribeAction},
	"cancel":   {method: http.MethodPost, handler: (*Server).handleTaskCancelAction},
	"resume":   {method: http.MethodPost, handler: (*Server).handleTaskResumeAction},
	"clone":    {method: http.MethodPost, handler: (*Server).handleTaskCloneAction},
}

var normalizedIntents = map[string]string{
	"task.list":          "task/list",
	"task/list":          "task/list",
	"task.describe":      "task/describe",
	"task/describe":      "task/describe",
	"task/[id]/describe": "task/describe",
	"task.cancel":        "task/cancel",
	"task/cancel":        "task/cancel",
	"task/[id]/cancel":   "task/cancel",
	"task.resume":        "task/resume",
	"task/resume":        "task/resume",
	"task/[id]/resume":   "task/resume",
	"task.clone":         "task/clone",
	"task/clone":         "task/clone",
	"task/[id]/clone":    "task/clone",
}

// NewServer constructs the HTTP entrypoint for the orchestrator services.
//
// The returned server is a thin coordinator: it validates and routes HTTP
// input, enriches requests with request metadata, and delegates domain work to
// the supplied services instead of owning planning or execution logic itself.
// Callers are responsible for providing already wired registry, task, runner,
// and optional LLM client dependencies.
func NewServer(config ServerConfig, registry *RegistryService, taskService *TaskService, runners *runner.TaskRunnerService, llmClient llm.Client, logger *slog.Logger) *Server {
	return &Server{
		config:    config,
		registry:  registry,
		tasks:     taskService,
		runners:   runners,
		llmClient: llmClient,
		logger:    logging.Component(logger, "orchestrator.server"),
	}
}

// ListenAndServe opens the configured listeners and serves the orchestrator API
// until shutdown.
//
// The serving workflow is: build the route mux, open the enabled TCP and Unix
// socket listeners, wrap requests with entrypoint metadata, serve each listener
// concurrently, and then block until the parent context is canceled or one of
// the listeners exits unexpectedly. On shutdown, ListenAndServe gives each
// listener a bounded finalize context so in-flight requests can stop cleanly
// while preserving the request metadata needed by downstream task creation.
func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v0/tools", s.handleTools)
	mux.HandleFunc("/v0/request", s.handleRequest)
	mux.HandleFunc("/v0/task/list", s.handleTaskList)
	mux.HandleFunc("/v0/tasks/run", s.handleTaskRuns)
	mux.HandleFunc("/v0/tasks", s.handleTasks)
	mux.HandleFunc("/v0/tasks/", s.handleTaskByID)

	listeners, err := s.openListeners(ctx, mux)
	if err != nil {
		return err
	}
	defer s.cleanupListeners(listeners)

	errCh := make(chan error, len(listeners))
	for _, item := range listeners {
		go func(item serverListener) {
			errCh <- item.server.Serve(item.listener)
		}(item)
	}

	s.logger.Info("server listening", "tcp", s.config.TCPAddress, "unix", s.config.UnixSocketPath)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := finalizeContext(ctx)
		defer cancel()
		for _, item := range listeners {
			if err := item.server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.logger.Error("server shutdown failed", "entrypoint", item.entrypoint, "error", err)
			}
		}
		return ctx.Err()
	case err := <-errCh:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) openListeners(ctx context.Context, mux http.Handler) ([]serverListener, error) {
	var listeners []serverListener
	cleanupOnError := func(openErr error) ([]serverListener, error) {
		s.cleanupListeners(listeners)
		return nil, openErr
	}

	if strings.TrimSpace(s.config.TCPAddress) != "" {
		listener, err := net.Listen("tcp", s.config.TCPAddress)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, serverListener{
			entrypoint: "tcp",
			listener:   listener,
			server: &http.Server{
				Handler:           s.withRequestMetadata(mux, "tcp"),
				ReadHeaderTimeout: serverReadHeaderTimeout,
				BaseContext: func(_ net.Listener) context.Context {
					return ctx
				},
			},
		})
	}

	if strings.TrimSpace(s.config.UnixSocketPath) != "" {
		if err := os.MkdirAll(filepath.Dir(s.config.UnixSocketPath), 0o755); err != nil {
			return cleanupOnError(err)
		}
		if err := removeUnixSocket(s.config.UnixSocketPath); err != nil {
			return cleanupOnError(err)
		}
		listener, err := net.Listen("unix", s.config.UnixSocketPath)
		if err != nil {
			return cleanupOnError(err)
		}
		listeners = append(listeners, serverListener{
			entrypoint: "unix",
			listener:   listener,
			server: &http.Server{
				Handler:           s.withRequestMetadata(mux, "unix"),
				ReadHeaderTimeout: serverReadHeaderTimeout,
				BaseContext: func(_ net.Listener) context.Context {
					return ctx
				},
			},
		})
		if err := os.Chmod(s.config.UnixSocketPath, 0o660); err != nil {
			return cleanupOnError(err)
		}
	}

	if len(listeners) == 0 {
		return nil, fmt.Errorf("at least one listener must be configured")
	}
	return listeners, nil
}

func (s *Server) cleanupListeners(listeners []serverListener) {
	for _, item := range listeners {
		_ = item.listener.Close()
	}
	if strings.TrimSpace(s.config.UnixSocketPath) != "" {
		_ = removeUnixSocket(s.config.UnixSocketPath)
	}
}

func removeUnixSocket(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}

	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("unix socket path %q already exists and is not a socket", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Server) withRequestMetadata(next http.Handler, entrypoint string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metadata := taskflow.RequestMetadata{
			Entrypoint: entrypoint,
			RemoteAddr: r.RemoteAddr,
			ReceivedAt: time.Now().UTC(),
		}
		if localAddr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && localAddr != nil {
			metadata.LocalAddr = localAddr.String()
		}
		next.ServeHTTP(w, r.WithContext(taskflow.WithRequestMetadata(r.Context(), metadata)))
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	tools, err := s.registry.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": tools})
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	payload, err := decodeRequestPayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if s.dispatchRequestIntent(w, r, payload) {
		return
	}

	s.createOrRunTaskFromPayload(w, r.Context(), payload)
}

func (s *Server) dispatchRequestIntent(w http.ResponseWriter, r *http.Request, payload requestPayload) bool {
	handler, ok := requestIntentHandlers[normalizeIntent(payload.Intent)]
	if !ok {
		return false
	}
	handler(s, w, r, payload)
	return true
}

func (s *Server) handleRequestTaskListIntent(w http.ResponseWriter, r *http.Request, _ requestPayload) {
	s.writeTaskList(w, r.Context())
}

func (s *Server) handleRequestTaskDescribeIntent(w http.ResponseWriter, r *http.Request, payload requestPayload) {
	s.describeTaskByID(w, r, payload.TaskID)
}

func (s *Server) handleRequestTaskCancelIntent(w http.ResponseWriter, r *http.Request, payload requestPayload) {
	s.cancelTaskByID(w, r, payload.TaskID)
}

func (s *Server) handleRequestTaskResumeIntent(w http.ResponseWriter, r *http.Request, payload requestPayload) {
	s.resumeTaskByID(w, r, payload.TaskID)
}

func (s *Server) handleRequestTaskCloneIntent(w http.ResponseWriter, r *http.Request, payload requestPayload) {
	s.cloneTaskByID(w, r, payload.TaskID, payload.CloneReason)
}

func (s *Server) createOrRunTaskFromPayload(w http.ResponseWriter, ctx context.Context, payload requestPayload) {
	request, err := understandIntent(ctx, s.llmClient, s.logger, requestEnvelopeFromPayload(payload))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if payload.AutoRun {
		s.startTaskRequest(w, ctx, request)
		return
	}
	s.createTaskRequest(w, ctx, request)
}

func (s *Server) createTaskRequest(w http.ResponseWriter, ctx context.Context, request taskflow.RequestEnvelope) {
	description, err := s.runners.Create(ctx, request)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, description)
}

func (s *Server) startTaskRequest(w http.ResponseWriter, ctx context.Context, request taskflow.RequestEnvelope) {
	description, err := s.runners.Start(ctx, request)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, description)
}

func (s *Server) writeTaskList(w http.ResponseWriter, ctx context.Context) {
	items, err := s.runners.List(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": items})
}

func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s.writeTaskList(w, r.Context())
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if !s.dispatchTaskCollection(w, r) {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) dispatchTaskCollection(w http.ResponseWriter, r *http.Request) bool {
	handler, ok := taskCollectionHandlers[r.Method]
	if !ok {
		return false
	}
	handler(s, w, r)
	return true
}

func (s *Server) handleTasksListMethod(w http.ResponseWriter, r *http.Request) {
	s.writeTaskList(w, r.Context())
}

func (s *Server) handleTasksCreateMethod(w http.ResponseWriter, r *http.Request) {
	payload, err := decodeRequestPayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.createOrRunTaskFromPayload(w, r.Context(), payload)
}

func (s *Server) handleTaskRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	payload, err := decodeRequestPayload(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	request, err := understandIntent(r.Context(), s.llmClient, s.logger, requestEnvelopeFromPayload(payload))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	s.startTaskRequest(w, r.Context(), request)
}

func (s *Server) handleTaskByID(w http.ResponseWriter, r *http.Request) {
	taskID, action, ok := parseTaskRoute(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "task id is required"})
		return
	}
	if !s.dispatchTaskAction(w, r, taskID, action) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown task action"})
	}
}

func (s *Server) dispatchTaskAction(w http.ResponseWriter, r *http.Request, taskID string, action string) bool {
	route, ok := taskActionHandlers[action]
	if !ok {
		return false
	}
	if r.Method != route.method {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return true
	}
	route.handler(s, w, r, taskID)
	return true
}

func (s *Server) handleTaskDescribeAction(w http.ResponseWriter, r *http.Request, taskID string) {
	s.describeTaskByID(w, r, taskID)
}

func (s *Server) handleTaskCancelAction(w http.ResponseWriter, r *http.Request, taskID string) {
	s.cancelTaskByID(w, r, taskID)
}

func (s *Server) handleTaskResumeAction(w http.ResponseWriter, r *http.Request, taskID string) {
	s.resumeTaskByID(w, r, taskID)
}

func (s *Server) handleTaskCloneAction(w http.ResponseWriter, r *http.Request, taskID string) {
	payload, err := decodeRequestPayload(r)
	if err != nil && !errors.Is(err, errEmptyBody) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.cloneTaskByID(w, r, taskID, payload.CloneReason)
}

func (s *Server) describeTaskByID(w http.ResponseWriter, r *http.Request, taskID string) {
	description, err := s.runners.Describe(r.Context(), taskID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, description)
}

func (s *Server) cancelTaskByID(w http.ResponseWriter, r *http.Request, taskID string) {
	description, err := s.runners.Cancel(r.Context(), taskID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, description)
}

func (s *Server) resumeTaskByID(w http.ResponseWriter, r *http.Request, taskID string) {
	description, err := s.runners.Resume(r.Context(), taskID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, description)
}

func (s *Server) cloneTaskByID(w http.ResponseWriter, r *http.Request, taskID string, reason string) {
	description, err := s.runners.Clone(r.Context(), taskID, reason)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, description)
}

var errEmptyBody = errors.New("empty request body")

func requestEnvelopeFromPayload(payload requestPayload) taskflow.RequestEnvelope {
	return taskflow.RequestEnvelope{Text: payload.Text, Parts: payload.Parts, Meta: payload.Meta}
}

func parseTaskRoute(path string) (taskID string, action string, ok bool) {
	trimmed := strings.TrimPrefix(path, "/v0/tasks/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return "", "", false
	}
	taskID = parts[0]
	if len(parts) > 1 {
		action = parts[1]
	}
	return taskID, action, true
}

func decodeRequestPayload(r *http.Request) (requestPayload, error) {
	defer r.Body.Close()
	if r.Body == nil {
		return requestPayload{}, errEmptyBody
	}
	var payload requestPayload
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&payload); err != nil {
		if errors.Is(err, os.ErrClosed) {
			return requestPayload{}, errEmptyBody
		}
		if strings.Contains(err.Error(), "EOF") {
			return requestPayload{}, errEmptyBody
		}
		return requestPayload{}, fmt.Errorf("decode request body: %w", err)
	}
	return payload, nil
}

func normalizeIntent(intent string) string {
	intent = strings.TrimSpace(strings.ToLower(intent))
	if normalized, ok := normalizedIntents[intent]; ok {
		return normalized
	}
	return intent
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}
