package runner

import (
	"strings"
	"time"

	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
)

// RequestPart captures one multimodal part of a task request.
type RequestPart struct {
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Text      string `json:"text,omitempty"`
	URI       string `json:"uri,omitempty"`
}

// RequestEnvelope stores the normalized task request payload.
type RequestEnvelope struct {
	Text  string            `json:"text,omitempty"`
	Parts []RequestPart     `json:"parts,omitempty"`
	Meta  map[string]string `json:"meta,omitempty"`
}

// RequestMetadata records how a request entered dango.
type RequestMetadata struct {
	Entrypoint string    `json:"entrypoint,omitempty"`
	RemoteAddr string    `json:"remote_addr,omitempty"`
	LocalAddr  string    `json:"local_addr,omitempty"`
	ReceivedAt time.Time `json:"received_at,omitempty"`
}

// TaskLineage records append-only task ancestry and revision state.
type TaskLineage struct {
	RootTaskID    string `json:"root_task_id,omitempty"`
	ParentTaskID  string `json:"parent_task_id,omitempty"`
	CloneOfTaskID string `json:"clone_of_task_id,omitempty"`
	Revision      int    `json:"revision,omitempty"`
}

// TaskMetadata stores structured metadata that is not part of the SQLite task row.
type TaskMetadata struct {
	Request RequestEnvelope `json:"request"`
	Entry   RequestMetadata `json:"entry"`
	Lineage TaskLineage     `json:"lineage"`
}

// TaskEvent is an append-only task lifecycle event.
type TaskEvent struct {
	Timestamp time.Time      `json:"timestamp"`
	Type      string         `json:"type"`
	Message   string         `json:"message,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// TaskSummary is the list-oriented view of a persisted task.
type TaskSummary struct {
	Task       sqlite.TaskRecord `json:"task"`
	Metadata   TaskMetadata      `json:"metadata"`
	TaskDir    string            `json:"task_dir"`
	ResultPath string            `json:"result_path"`
}

// TaskDescription is the detailed view of a persisted task.
type TaskDescription struct {
	TaskSummary
	Plan   spec.DAGPlan `json:"plan"`
	Events []TaskEvent  `json:"events"`
}

// TaskRunResult summarizes a completed task runner execution.
type TaskRunResult struct {
	// Task is the final persisted task row.
	Task sqlite.TaskRecord `json:"task"`
	// Plan is the plan executed for the task.
	Plan spec.DAGPlan `json:"plan"`
	// TerminalHandoffs contains frontmatter summaries from terminal edges.
	TerminalHandoffs []spec.HandoffMetadata `json:"terminal_handoffs"`
	// TaskDir is the task directory on disk.
	TaskDir string `json:"task_dir"`
	// ResultPath is the result.md path written by the runner.
	ResultPath string `json:"result_path"`
}

func primaryRequestText(request RequestEnvelope) string {
	if strings.TrimSpace(request.Text) != "" {
		return strings.TrimSpace(request.Text)
	}
	for _, part := range request.Parts {
		if strings.TrimSpace(part.Text) != "" {
			return strings.TrimSpace(part.Text)
		}
	}
	return ""
}

func mergeRequestMetadata(base, overlay RequestMetadata) RequestMetadata {
	if strings.TrimSpace(base.Entrypoint) == "" {
		base.Entrypoint = overlay.Entrypoint
	}
	if strings.TrimSpace(base.RemoteAddr) == "" {
		base.RemoteAddr = overlay.RemoteAddr
	}
	if strings.TrimSpace(base.LocalAddr) == "" {
		base.LocalAddr = overlay.LocalAddr
	}
	if base.ReceivedAt.IsZero() {
		base.ReceivedAt = overlay.ReceivedAt
	}
	return base
}
