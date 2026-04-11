package orchestrator

import (
	"context"
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

// activeListener pairs a named network listener with the HTTP server that serves it.
type activeListener struct {
	name     string
	listener net.Listener
	server   *http.Server
}

// taskRequest is the JSON body accepted by the orchestrator API endpoints.
type taskRequest struct {
	Intent      string                 `json:"intent,omitempty"`
	TaskID      string                 `json:"task_id,omitempty"`
	Text        string                 `json:"text,omitempty"`
	Parts       []taskflow.RequestPart `json:"parts,omitempty"`
	Meta        map[string]string      `json:"meta,omitempty"`
	AutoRun     bool                   `json:"auto_run,omitempty"`
	CloneReason string                 `json:"clone_reason,omitempty"`
}

const readHeaderTimeout = 5 * time.Second

// intentHandlers maps canonical intent strings to their in-request handler
// functions, used by the generic /v0/request endpoint.
var intentHandlers = map[string]func(*Server, *gin.Context, taskRequest){
	"task/list":     func(s *Server, c *gin.Context, _ taskRequest) { s.listTasks(c) },
	"task/describe": func(s *Server, c *gin.Context, r taskRequest) { s.getTaskByID(c, r.TaskID) },
	"task/cancel":   func(s *Server, c *gin.Context, r taskRequest) { s.cancelTaskByID(c, r.TaskID) },
	"task/resume":   func(s *Server, c *gin.Context, r taskRequest) { s.resumeTaskByID(c, r.TaskID) },
	"task/clone":    func(s *Server, c *gin.Context, r taskRequest) { s.cloneTaskByID(c, r.TaskID, r.CloneReason) },
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

// buildRouter constructs a gin router with all API routes registered.
//
// Each listener calls buildRouter with its own name so the request-metadata
// middleware tags requests with the correct origin.
func (s *Server) buildRouter(name string) *gin.Engine {
	router := gin.New()
	router.Use(s.requestMetadata(name))

	router.GET("/healthz", s.healthz)

	v0 := router.Group("/v0")
	{
		v0.GET("/tools", s.listTools)
		v0.POST("/request", s.handleRequest)
		v0.GET("/task/list", s.listTasks)
		v0.POST("/tasks/run", s.runTask)

		tasks := v0.Group("/tasks")
		{
			tasks.GET("", s.listTasks)
			tasks.POST("", s.createTask)
			tasks.GET("/:id", s.getTask)
			tasks.GET("/:id/describe", s.getTask)
			tasks.POST("/:id/cancel", s.cancelTask)
			tasks.POST("/:id/resume", s.resumeTask)
			tasks.POST("/:id/clone", s.cloneTask)
		}
	}

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
	for _, l := range listeners {
		go func(l activeListener) {
			errCh <- l.server.Serve(l.listener)
		}(l)
	}

	s.logger.Info("server listening", "tcp", s.config.TCPAddress, "unix", s.config.UnixSocketPath)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := finalizeContext(ctx)
		defer cancel()
		for _, l := range listeners {
			if err := l.server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.logger.Error("server shutdown failed", "listener", l.name, "error", err)
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

func (s *Server) openListeners(ctx context.Context) ([]activeListener, error) {
	var listeners []activeListener
	cleanupOnError := func(openErr error) ([]activeListener, error) {
		s.cleanupListeners(listeners)
		return nil, openErr
	}

	if strings.TrimSpace(s.config.TCPAddress) != "" {
		ln, err := net.Listen("tcp", s.config.TCPAddress)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, activeListener{
			name:     "tcp",
			listener: ln,
			server: &http.Server{
				Handler:           s.buildRouter("tcp"),
				ReadHeaderTimeout: readHeaderTimeout,
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
		ln, err := net.Listen("unix", s.config.UnixSocketPath)
		if err != nil {
			return cleanupOnError(err)
		}
		listeners = append(listeners, activeListener{
			name:     "unix",
			listener: ln,
			server: &http.Server{
				Handler:           s.buildRouter("unix"),
				ReadHeaderTimeout: readHeaderTimeout,
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

func (s *Server) cleanupListeners(listeners []activeListener) {
	for _, l := range listeners {
		_ = l.listener.Close()
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

// requestMetadata returns a gin middleware that enriches each request context
// with [taskflow.RequestMetadata] identifying the listener by name.
func (s *Server) requestMetadata(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		md := taskflow.RequestMetadata{
			Entrypoint: name,
			RemoteAddr: c.Request.RemoteAddr,
			ReceivedAt: time.Now().UTC(),
		}
		if localAddr, ok := c.Request.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && localAddr != nil {
			md.LocalAddr = localAddr.String()
		}
		c.Request = c.Request.WithContext(taskflow.WithRequestMetadata(c.Request.Context(), md))
		c.Next()
	}
}

func (s *Server) healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Server) listTools(c *gin.Context) {
	tools, err := s.registry.List(c.Request.Context())
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tools": tools})
}

func (s *Server) handleRequest(c *gin.Context) {
	var req taskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if fn, ok := intentHandlers[normalizeIntent(req.Intent)]; ok {
		fn(s, c, req)
		return
	}

	s.submitTask(c, req)
}

func (s *Server) listTasks(c *gin.Context) {
	items, err := s.runners.List(c.Request.Context())
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": items})
}

func (s *Server) createTask(c *gin.Context) {
	var req taskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.submitTask(c, req)
}

func (s *Server) runTask(c *gin.Context) {
	var req taskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	envelope := taskflow.RequestEnvelope{Text: req.Text, Parts: req.Parts, Meta: req.Meta}
	request, err := understandIntent(c.Request.Context(), s.llmClient, s.logger, envelope)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	description, err := s.runners.Start(c.Request.Context(), request)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, description)
}

func (s *Server) getTask(c *gin.Context) {
	s.getTaskByID(c, c.Param("id"))
}

func (s *Server) cancelTask(c *gin.Context) {
	s.cancelTaskByID(c, c.Param("id"))
}

func (s *Server) resumeTask(c *gin.Context) {
	s.resumeTaskByID(c, c.Param("id"))
}

func (s *Server) cloneTask(c *gin.Context) {
	var req taskRequest
	_ = c.ShouldBindJSON(&req) // body is optional; clone reason may be omitted
	s.cloneTaskByID(c, c.Param("id"), req.CloneReason)
}

// submitTask resolves the request intent and either creates or immediately starts
// a task, depending on the AutoRun flag.
func (s *Server) submitTask(c *gin.Context, req taskRequest) {
	envelope := taskflow.RequestEnvelope{Text: req.Text, Parts: req.Parts, Meta: req.Meta}
	request, err := understandIntent(c.Request.Context(), s.llmClient, s.logger, envelope)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if req.AutoRun {
		description, err := s.runners.Start(c.Request.Context(), request)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, description)
		return
	}
	description, err := s.runners.Create(c.Request.Context(), request)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, description)
}

func (s *Server) getTaskByID(c *gin.Context, id string) {
	description, err := s.runners.Describe(c.Request.Context(), id)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, description)
}

func (s *Server) cancelTaskByID(c *gin.Context, id string) {
	description, err := s.runners.Cancel(c.Request.Context(), id)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, description)
}

func (s *Server) resumeTaskByID(c *gin.Context, id string) {
	description, err := s.runners.Resume(c.Request.Context(), id)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, description)
}

func (s *Server) cloneTaskByID(c *gin.Context, id string, reason string) {
	description, err := s.runners.Clone(c.Request.Context(), id, reason)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, description)
}

func normalizeIntent(intent string) string {
	intent = strings.TrimSpace(strings.ToLower(intent))
	if normalized, ok := normalizedIntents[intent]; ok {
		return normalized
	}
	return intent
}
