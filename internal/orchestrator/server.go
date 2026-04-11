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

	"github.com/gin-gonic/gin"
	"github.com/tsumina/dango/internal/ai"
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
	llmClient ai.Client
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

type requestIntentHandler func(*Server, *gin.Context, requestPayload)

const serverReadHeaderTimeout = 5 * time.Second

var requestIntentHandlers = map[string]requestIntentHandler{
	"task/list":     (*Server).handleRequestTaskListIntent,
	"task/describe": (*Server).handleRequestTaskDescribeIntent,
	"task/cancel":   (*Server).handleRequestTaskCancelIntent,
	"task/resume":   (*Server).handleRequestTaskResumeIntent,
	"task/clone":    (*Server).handleRequestTaskCloneIntent,
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
func NewServer(config ServerConfig, registry *RegistryService, taskService *TaskService, runners *runner.TaskRunnerService, llmClient ai.Client, logger *slog.Logger) *Server {
	return &Server{
		config:    config,
		registry:  registry,
		tasks:     taskService,
		runners:   runners,
		llmClient: llmClient,
		logger:    logging.Component(logger, "orchestrator.server"),
	}
}

// buildRouter constructs a gin router with all API routes registered.
//
// Each listener calls buildRouter with its own entrypoint label so the
// request-metadata middleware tags requests with the correct origin.
func (s *Server) buildRouter(entrypoint string) *gin.Engine {
	router := gin.New()
	router.Use(s.requestMetadataMiddleware(entrypoint))

	router.GET("/healthz", s.handleHealth)
	router.GET("/v0/tools", s.handleTools)
	router.POST("/v0/request", s.handleRequest)
	router.GET("/v0/task/list", s.handleTaskList)
	router.POST("/v0/tasks/run", s.handleTaskRuns)
	router.GET("/v0/tasks", s.handleTasksList)
	router.POST("/v0/tasks", s.handleTasksCreate)
	router.GET("/v0/tasks/:id", s.handleTaskDescribe)
	router.GET("/v0/tasks/:id/describe", s.handleTaskDescribe)
	router.POST("/v0/tasks/:id/cancel", s.handleTaskCancel)
	router.POST("/v0/tasks/:id/resume", s.handleTaskResume)
	router.POST("/v0/tasks/:id/clone", s.handleTaskClone)

	return router
}

// ListenAndServe opens the configured listeners and serves the orchestrator API
// until shutdown.
//
// The serving workflow is: build the gin router per listener, open the enabled
// TCP and Unix socket listeners, serve each listener concurrently, and then
// block until the parent context is canceled or one of the listeners exits
// unexpectedly. On shutdown, ListenAndServe gives each listener a bounded
// finalize context so in-flight requests can stop cleanly while preserving the
// request metadata needed by downstream task creation.
func (s *Server) ListenAndServe(ctx context.Context) error {
	listeners, err := s.openListeners(ctx)
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

func (s *Server) openListeners(ctx context.Context) ([]serverListener, error) {
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
				Handler:           s.buildRouter("tcp"),
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
				Handler:           s.buildRouter("unix"),
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

// requestMetadataMiddleware returns a gin middleware that enriches each request
// context with [taskflow.RequestMetadata] identifying the listener entrypoint.
func (s *Server) requestMetadataMiddleware(entrypoint string) gin.HandlerFunc {
	return func(c *gin.Context) {
		metadata := taskflow.RequestMetadata{
			Entrypoint: entrypoint,
			RemoteAddr: c.Request.RemoteAddr,
			ReceivedAt: time.Now().UTC(),
		}
		if localAddr, ok := c.Request.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && localAddr != nil {
			metadata.LocalAddr = localAddr.String()
		}
		ctx := taskflow.WithRequestMetadata(c.Request.Context(), metadata)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func (s *Server) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleTools(c *gin.Context) {
	tools, err := s.registry.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"tools": tools})
}

func (s *Server) handleRequest(c *gin.Context) {
	payload, err := decodeRequestPayload(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if s.dispatchRequestIntent(c, payload) {
		return
	}

	s.createOrRunTaskFromPayload(c, payload)
}

func (s *Server) dispatchRequestIntent(c *gin.Context, payload requestPayload) bool {
	handler, ok := requestIntentHandlers[normalizeIntent(payload.Intent)]
	if !ok {
		return false
	}
	handler(s, c, payload)
	return true
}

func (s *Server) handleRequestTaskListIntent(c *gin.Context, _ requestPayload) {
	s.writeTaskList(c)
}

func (s *Server) handleRequestTaskDescribeIntent(c *gin.Context, payload requestPayload) {
	s.describeTaskByID(c, payload.TaskID)
}

func (s *Server) handleRequestTaskCancelIntent(c *gin.Context, payload requestPayload) {
	s.cancelTaskByID(c, payload.TaskID)
}

func (s *Server) handleRequestTaskResumeIntent(c *gin.Context, payload requestPayload) {
	s.resumeTaskByID(c, payload.TaskID)
}

func (s *Server) handleRequestTaskCloneIntent(c *gin.Context, payload requestPayload) {
	s.cloneTaskByID(c, payload.TaskID, payload.CloneReason)
}

func (s *Server) createOrRunTaskFromPayload(c *gin.Context, payload requestPayload) {
	request, err := understandIntent(c.Request.Context(), s.llmClient, s.logger, requestEnvelopeFromPayload(payload))
	if err != nil {
		c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if payload.AutoRun {
		s.startTaskRequest(c, request)
		return
	}
	s.createTaskRequest(c, request)
}

func (s *Server) createTaskRequest(c *gin.Context, request taskflow.RequestEnvelope) {
	description, err := s.runners.Create(c.Request.Context(), request)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, description)
}

func (s *Server) startTaskRequest(c *gin.Context, request taskflow.RequestEnvelope) {
	description, err := s.runners.Start(c.Request.Context(), request)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, description)
}

func (s *Server) writeTaskList(c *gin.Context) {
	items, err := s.runners.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"tasks": items})
}

func (s *Server) handleTaskList(c *gin.Context) {
	s.writeTaskList(c)
}

func (s *Server) handleTasksList(c *gin.Context) {
	s.writeTaskList(c)
}

func (s *Server) handleTasksCreate(c *gin.Context) {
	payload, err := decodeRequestPayload(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.createOrRunTaskFromPayload(c, payload)
}

func (s *Server) handleTaskRuns(c *gin.Context) {
	payload, err := decodeRequestPayload(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	request, err := understandIntent(c.Request.Context(), s.llmClient, s.logger, requestEnvelopeFromPayload(payload))
	if err != nil {
		c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	s.startTaskRequest(c, request)
}

func (s *Server) handleTaskDescribe(c *gin.Context) {
	s.describeTaskByID(c, c.Param("id"))
}

func (s *Server) handleTaskCancel(c *gin.Context) {
	s.cancelTaskByID(c, c.Param("id"))
}

func (s *Server) handleTaskResume(c *gin.Context) {
	s.resumeTaskByID(c, c.Param("id"))
}

func (s *Server) handleTaskClone(c *gin.Context) {
	payload, err := decodeRequestPayload(c.Request)
	if err != nil && !errors.Is(err, errEmptyBody) {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.cloneTaskByID(c, c.Param("id"), payload.CloneReason)
}

func (s *Server) describeTaskByID(c *gin.Context, taskID string) {
	description, err := s.runners.Describe(c.Request.Context(), taskID)
	if err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, description)
}

func (s *Server) cancelTaskByID(c *gin.Context, taskID string) {
	description, err := s.runners.Cancel(c.Request.Context(), taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, description)
}

func (s *Server) resumeTaskByID(c *gin.Context, taskID string) {
	description, err := s.runners.Resume(c.Request.Context(), taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, description)
}

func (s *Server) cloneTaskByID(c *gin.Context, taskID string, reason string) {
	description, err := s.runners.Clone(c.Request.Context(), taskID, reason)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, description)
}

var errEmptyBody = errors.New("empty request body")

func requestEnvelopeFromPayload(payload requestPayload) taskflow.RequestEnvelope {
	return taskflow.RequestEnvelope{Text: payload.Text, Parts: payload.Parts, Meta: payload.Meta}
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
