package orchestrator

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/tsumina/dango/internal/llm"
	"github.com/tsumina/dango/internal/runner"
	"github.com/tsumina/dango/internal/taskflow"
)

type httpRouteDeps struct {
	registry  *RegistryService
	runners   *runner.TaskRunnerService
	llmClient llm.Client
	logger    *slog.Logger
}

type requestPartRequest struct {
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Text      string `json:"text,omitempty" binding:"required_without=URI"`
	URI       string `json:"uri,omitempty" binding:"required_without=Text"`
}

type taskSubmissionRequest struct {
	Text    string               `json:"text,omitempty" binding:"required_without=Parts"`
	Parts   []requestPartRequest `json:"parts,omitempty" binding:"required_without=Text,dive"`
	Meta    map[string]string    `json:"meta,omitempty"`
	AutoRun bool                 `json:"auto_run,omitempty"`
}

type taskRunRequest struct {
	Text  string               `json:"text,omitempty" binding:"required_without=Parts"`
	Parts []requestPartRequest `json:"parts,omitempty" binding:"required_without=Text,dive"`
	Meta  map[string]string    `json:"meta,omitempty"`
}

type taskIDURI struct {
	ID string `uri:"id" binding:"required"`
}

type cloneTaskRequest struct {
	CloneReason string `json:"clone_reason,omitempty"`
}

func (r taskSubmissionRequest) envelope() taskflow.RequestEnvelope {
	return taskflow.RequestEnvelope{
		Text:  r.Text,
		Parts: toRequestParts(r.Parts),
		Meta:  r.Meta,
	}
}

func (r taskRunRequest) envelope() taskflow.RequestEnvelope {
	return taskflow.RequestEnvelope{
		Text:  r.Text,
		Parts: toRequestParts(r.Parts),
		Meta:  r.Meta,
	}
}

func toRequestParts(parts []requestPartRequest) []taskflow.RequestPart {
	out := make([]taskflow.RequestPart, 0, len(parts))
	for _, part := range parts {
		out = append(out, taskflow.RequestPart{
			Kind:      part.Kind,
			Name:      part.Name,
			MediaType: part.MediaType,
			Text:      part.Text,
			URI:       part.URI,
		})
	}
	return out
}

func bindJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return false
	}
	return true
}

func bindURI(c *gin.Context, target any) bool {
	if err := c.ShouldBindUri(target); err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return false
	}
	return true
}

func bindOptionalJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return false
	}
	return true
}

func serveHealthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func serveListTools(c *gin.Context, deps httpRouteDeps) {
	if deps.registry == nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "registry service is not configured"})
		return
	}

	tools, err := deps.registry.List(c.Request.Context())
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tools": tools})
}

func serveListTasks(c *gin.Context, deps httpRouteDeps) {
	if deps.runners == nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "task runner service is not configured"})
		return
	}

	items, err := deps.runners.List(c.Request.Context())
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": items})
}

func serveSubmitTask(c *gin.Context, deps httpRouteDeps, req taskSubmissionRequest) {
	if deps.runners == nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "task runner service is not configured"})
		return
	}

	request, err := understandIntent(c.Request.Context(), deps.llmClient, deps.logger, req.envelope())
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	if req.AutoRun {
		description, err := deps.runners.Start(c.Request.Context(), request)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusAccepted, description)
		return
	}

	description, err := deps.runners.Create(c.Request.Context(), request)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, description)
}

func serveRunTask(c *gin.Context, deps httpRouteDeps, req taskRunRequest) {
	if deps.runners == nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "task runner service is not configured"})
		return
	}

	request, err := understandIntent(c.Request.Context(), deps.llmClient, deps.logger, req.envelope())
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	description, err := deps.runners.Start(c.Request.Context(), request)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, description)
}

func serveGetTask(c *gin.Context, deps httpRouteDeps, uri taskIDURI) {
	if deps.runners == nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "task runner service is not configured"})
		return
	}

	description, err := deps.runners.Describe(c.Request.Context(), uri.ID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, description)
}

func serveCancelTask(c *gin.Context, deps httpRouteDeps, uri taskIDURI) {
	if deps.runners == nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "task runner service is not configured"})
		return
	}

	description, err := deps.runners.Cancel(c.Request.Context(), uri.ID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, description)
}

func serveResumeTask(c *gin.Context, deps httpRouteDeps, uri taskIDURI) {
	if deps.runners == nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "task runner service is not configured"})
		return
	}

	description, err := deps.runners.Resume(c.Request.Context(), uri.ID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, description)
}

func serveCloneTask(c *gin.Context, deps httpRouteDeps, uri taskIDURI, req cloneTaskRequest) {
	if deps.runners == nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "task runner service is not configured"})
		return
	}

	description, err := deps.runners.Clone(c.Request.Context(), uri.ID, req.CloneReason)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, description)
}
