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

const readHeaderTimeout = 5 * time.Second

// NewServer constructs the HTTP entrypoint for the orchestrator services.
//
// The returned server is a thin transport shell: it owns listener lifecycle,
// request metadata middleware, and router construction, while delegating HTTP
// request handling to package-level route handlers. Callers are responsible for
// providing already wired registry, runner, and optional LLM client
// dependencies.
func NewServer(config ServerConfig, registry *RegistryService, runners *runner.TaskRunnerService, llmClient llm.Client, logger *slog.Logger) *Server {
	return &Server{
		config:    config,
		registry:  registry,
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

	deps := httpRouteDeps{
		registry:  s.registry,
		runners:   s.runners,
		llmClient: s.llmClient,
		logger:    s.logger,
	}

	router.GET("/healthz", func(c *gin.Context) {
		serveHealthz(c)
	})

	v0 := router.Group("/v0")
	{
		v0.GET("/tools", func(c *gin.Context) {
			serveListTools(c, deps)
		})
		v0.POST("/request", func(c *gin.Context) {
			var req taskSubmissionRequest
			if !bindJSON(c, &req) {
				return
			}
			serveSubmitTask(c, deps, req)
		})
		v0.POST("/tasks/run", func(c *gin.Context) {
			var req taskRunRequest
			if !bindJSON(c, &req) {
				return
			}
			serveRunTask(c, deps, req)
		})

		tasks := v0.Group("/tasks")
		{
			tasks.GET("", func(c *gin.Context) {
				serveListTasks(c, deps)
			})
			tasks.POST("", func(c *gin.Context) {
				var req taskSubmissionRequest
				if !bindJSON(c, &req) {
					return
				}
				serveSubmitTask(c, deps, req)
			})
			tasks.GET("/:id", func(c *gin.Context) {
				var uri taskIDURI
				if !bindURI(c, &uri) {
					return
				}
				serveGetTask(c, deps, uri)
			})
			tasks.POST("/:id/cancel", func(c *gin.Context) {
				var uri taskIDURI
				if !bindURI(c, &uri) {
					return
				}
				serveCancelTask(c, deps, uri)
			})
			tasks.POST("/:id/resume", func(c *gin.Context) {
				var uri taskIDURI
				if !bindURI(c, &uri) {
					return
				}
				serveResumeTask(c, deps, uri)
			})
			tasks.POST("/:id/clone", func(c *gin.Context) {
				var uri taskIDURI
				if !bindURI(c, &uri) {
					return
				}
				var req cloneTaskRequest
				if !bindOptionalJSON(c, &req) {
					return
				}
				serveCloneTask(c, deps, uri, req)
			})
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
