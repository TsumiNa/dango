package taskflow

import (
	"strings"
	"time"

	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
)

// RequestPart captures one multimodal fragment of a task request.
//
// A request may contain a primary free-form text plus zero or more structured
// parts that point at attachments or alternate representations.
type RequestPart struct {
	// Kind identifies the logical part category, such as text or attachment.
	Kind string `json:"kind,omitempty"`
	// Name is an optional human-readable label for the part.
	Name string `json:"name,omitempty"`
	// MediaType records the MIME type for non-text parts when known.
	MediaType string `json:"media_type,omitempty"`
	// Text stores inline text content carried by this part.
	Text string `json:"text,omitempty"`
	// URI points at an external or local resource associated with this part.
	URI string `json:"uri,omitempty"`
}

// RequestEnvelope stores the normalized task request payload that moves between
// the orchestrator and runner.
type RequestEnvelope struct {
	// Text is the primary free-form request text.
	Text string `json:"text,omitempty"`
	// Parts stores any multimodal request fragments associated with Text.
	Parts []RequestPart `json:"parts,omitempty"`
	// Meta stores structured request metadata propagated across the workflow.
	Meta map[string]string `json:"meta,omitempty"`
}

// RequestMetadata records how a request entered dango.
//
// This metadata is captured at ingress and is later persisted into task
// metadata so the control plane can explain where a task came from.
type RequestMetadata struct {
	// Entrypoint identifies the API surface or listener that accepted the request.
	Entrypoint string `json:"entrypoint,omitempty"`
	// RemoteAddr records the remote client address when available.
	RemoteAddr string `json:"remote_addr,omitempty"`
	// LocalAddr records the local listener address that accepted the request.
	LocalAddr string `json:"local_addr,omitempty"`
	// ReceivedAt records when the request entered the control plane.
	ReceivedAt time.Time `json:"received_at,omitempty"`
}

// TaskLineage records append-only task ancestry and revision state.
//
// It lets clone-and-revise flows preserve a stable root task while recording
// parent-child relationships between revisions.
type TaskLineage struct {
	// RootTaskID identifies the original task lineage root.
	RootTaskID string `json:"root_task_id,omitempty"`
	// ParentTaskID identifies the immediate parent task when this task was derived.
	ParentTaskID string `json:"parent_task_id,omitempty"`
	// CloneOfTaskID identifies the task explicitly cloned to create this task.
	CloneOfTaskID string `json:"clone_of_task_id,omitempty"`
	// Revision counts lineage revisions starting at 1.
	Revision int `json:"revision,omitempty"`
}

// TaskMetadata stores structured task metadata that does not live directly in
// the SQLite task row.
type TaskMetadata struct {
	// Request is the normalized request envelope associated with the task.
	Request RequestEnvelope `json:"request"`
	// Entry records how the request entered the system.
	Entry RequestMetadata `json:"entry"`
	// Lineage records ancestry and revision relationships for the task.
	Lineage TaskLineage `json:"lineage"`
}

// TaskEvent is one append-only task lifecycle event.
//
// TaskService appends these events to a JSONL log so task history can be
// reconstructed without mutating past entries.
type TaskEvent struct {
	// Timestamp records when the event occurred.
	Timestamp time.Time `json:"timestamp"`
	// Type identifies the machine-readable event category.
	Type string `json:"type"`
	// Message is the human-readable event summary.
	Message string `json:"message,omitempty"`
	// Data contains optional structured event fields.
	Data map[string]any `json:"data,omitempty"`
}

// TaskSummary is the list-oriented view of a persisted task.
//
// It combines the SQLite task row with enough metadata and path information to
// render list and index views without loading the full plan and event log.
type TaskSummary struct {
	// Task is the persisted SQLite task row.
	Task sqlite.TaskRecord `json:"task"`
	// Metadata is the structured task metadata sidecar.
	Metadata TaskMetadata `json:"metadata"`
	// TaskDir is the task root directory on disk.
	TaskDir string `json:"task_dir"`
	// ResultPath points at the task's result artifact.
	ResultPath string `json:"result_path"`
}

// TaskDescription is the detailed persisted view of a task.
//
// It extends [TaskSummary] with the decoded DAG plan and append-only event log.
type TaskDescription struct {
	TaskSummary
	// Plan is the decoded persisted DAG plan, when available.
	Plan spec.DAGPlan `json:"plan"`
	// Events is the decoded append-only lifecycle event log.
	Events []TaskEvent `json:"events"`
}

// TaskRunResult summarizes one completed synchronous task run.
//
// It is the terminal view returned by runner.RunNow and TaskRunner.Run.
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

// NormalizeRequestEnvelope trims and normalizes a request envelope.
//
// Empty parts are removed, string fields are trimmed, and Meta is always
// initialized so later workflow stages can safely add metadata.
func NormalizeRequestEnvelope(request RequestEnvelope) RequestEnvelope {
	request.Text = strings.TrimSpace(request.Text)
	if request.Meta == nil {
		request.Meta = map[string]string{}
	}
	parts := make([]RequestPart, 0, len(request.Parts))
	for _, part := range request.Parts {
		if strings.TrimSpace(part.Text) == "" && strings.TrimSpace(part.URI) == "" {
			continue
		}
		part.Kind = strings.TrimSpace(part.Kind)
		part.Name = strings.TrimSpace(part.Name)
		part.MediaType = strings.TrimSpace(part.MediaType)
		part.Text = strings.TrimSpace(part.Text)
		part.URI = strings.TrimSpace(part.URI)
		parts = append(parts, part)
	}
	request.Parts = parts
	return request
}

// PrimaryRequestText extracts the primary free-form text from a request.
//
// The function prefers RequestEnvelope.Text and falls back to the first part
// that still carries text content after normalization.
func PrimaryRequestText(request RequestEnvelope) string {
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

// MergeRequestMetadata merges missing fields from overlay into base.
//
// Existing values in base win so that earlier ingress information is preserved
// unless it was absent.
func MergeRequestMetadata(base, overlay RequestMetadata) RequestMetadata {
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
