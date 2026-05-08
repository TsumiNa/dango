package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	storepkg "github.com/tsumina/dango/internal/store"
)

// DescribeNode is the describe-facing projection of one runner node.
type DescribeNode struct {
	ID              string   `json:"id" yaml:"id"`
	SkillName       string   `json:"skill_name,omitempty" yaml:"skill_name,omitempty"`
	TaskDescription string   `json:"task_description,omitempty" yaml:"task_description,omitempty"`
	DependsOn       []string `json:"depends_on,omitempty" yaml:"depends_on,omitempty"`
	Status          string   `json:"status,omitempty" yaml:"status,omitempty"`
	Error           string   `json:"error,omitempty" yaml:"error,omitempty"`
}

// DescribeArtifact is the describe-facing projection of one emitted artifact.
type DescribeArtifact struct {
	NodeID       string `json:"node_id,omitempty" yaml:"node_id,omitempty"`
	Path         string `json:"path" yaml:"path"`
	ResourceType string `json:"resource_type,omitempty" yaml:"resource_type,omitempty"`
	Description  string `json:"description,omitempty" yaml:"description,omitempty"`
	Stage        string `json:"stage,omitempty" yaml:"stage,omitempty"`
}

// DescribeView is the materialized request-level describe state built from the
// request event log.
type DescribeView struct {
	RequestID                string                  `json:"request_id" yaml:"request_id"`
	RunnerID                 string                  `json:"runner_id,omitempty" yaml:"runner_id,omitempty"`
	Phase                    runnerpkg.RunnerPhase   `json:"phase,omitempty" yaml:"phase,omitempty"`
	Status                   runnerpkg.RunnerStatus  `json:"status,omitempty" yaml:"status,omitempty"`
	Nodes                    map[string]DescribeNode `json:"nodes,omitempty" yaml:"nodes,omitempty"`
	Artifacts                []DescribeArtifact      `json:"artifacts,omitempty" yaml:"artifacts,omitempty"`
	LatestCheckpointSequence int64                   `json:"latest_checkpoint_sequence,omitempty" yaml:"latest_checkpoint_sequence,omitempty"`
	LatestEventSequence      uint64                  `json:"latest_event_sequence,omitempty" yaml:"latest_event_sequence,omitempty"`
}

// ReplayDescribeView replays request event-log frames after cursor into view.
//
// When view is nil, ReplayDescribeView starts from a fresh describe state.
// Callers that already materialized prior frames can pass the existing view
// plus its persisted cursor to continue replay without duplicating state.
func ReplayDescribeView(ctx context.Context, requestID string, view *DescribeView, cursor storepkg.SnapshotCursor, eventLog storepkg.EventLogStore) (*DescribeView, error) {
	if eventLog == nil {
		return nil, fmt.Errorf("orchestrate: ReplayDescribeView requires a non-nil event log store")
	}
	if requestID == "" {
		requestID = cursor.RequestID
	}
	if requestID == "" {
		return nil, fmt.Errorf("orchestrate: ReplayDescribeView requires a request id")
	}
	if cursor.RequestID != "" && cursor.RequestID != requestID {
		return nil, fmt.Errorf("orchestrate: describe cursor request %q does not match %q", cursor.RequestID, requestID)
	}
	if view == nil {
		view = &DescribeView{RequestID: requestID, Nodes: make(map[string]DescribeNode)}
	}
	if view.RequestID == "" {
		view.RequestID = requestID
	}
	if view.RequestID != requestID {
		return nil, fmt.Errorf("orchestrate: describe view request %q does not match %q", view.RequestID, requestID)
	}
	if view.Nodes == nil {
		view.Nodes = make(map[string]DescribeNode)
	}
	if cursor.CheckpointSequence > view.LatestCheckpointSequence {
		view.LatestCheckpointSequence = cursor.CheckpointSequence
	}
	if cursor.EventSequence > view.LatestEventSequence {
		view.LatestEventSequence = cursor.EventSequence
	}

	from := uint64(1)
	if cursor.EventSequence > 0 {
		from = cursor.EventSequence + 1
	}
	rawEvents, err := eventLog.LoadEvents(ctx, streampkg.Scope{RequestID: requestID}, from, streampkg.Filter{})
	if err != nil {
		return nil, fmt.Errorf("orchestrate: load describe replay for %q: %w", requestID, err)
	}
	for _, raw := range rawEvents {
		events, err := streampkg.ExpandBundleEvent(raw)
		if err != nil {
			return nil, fmt.Errorf("orchestrate: expand describe replay %q/%d: %w", requestID, raw.SequenceNumber, err)
		}
		for _, event := range events {
			if err := view.applyEvent(event); err != nil {
				return nil, fmt.Errorf("orchestrate: apply describe replay %q/%d: %w", requestID, raw.SequenceNumber, err)
			}
		}
		if raw.SequenceNumber > view.LatestEventSequence {
			view.LatestEventSequence = raw.SequenceNumber
		}
	}
	if view.RequestID == "" {
		view.RequestID = requestID
	}
	return view, nil
}

// SnapshotCursor returns the replay cursor that corresponds to the current
// describe view.
func (v *DescribeView) SnapshotCursor() storepkg.SnapshotCursor {
	if v == nil {
		return storepkg.SnapshotCursor{}
	}
	return storepkg.SnapshotCursor{
		RequestID:          v.RequestID,
		RunnerID:           v.RunnerID,
		CheckpointSequence: v.LatestCheckpointSequence,
		EventSequence:      v.LatestEventSequence,
	}
}

func (v *DescribeView) applyEvent(event streampkg.Event) error {
	if v == nil {
		return fmt.Errorf("nil describe view")
	}
	if event.Scope.RequestID != "" {
		if v.RequestID == "" {
			v.RequestID = event.Scope.RequestID
		} else if v.RequestID != event.Scope.RequestID {
			return fmt.Errorf("event request %q does not match describe view request %q", event.Scope.RequestID, v.RequestID)
		}
	}
	if event.Scope.RunnerID != "" && v.RunnerID == "" {
		v.RunnerID = event.Scope.RunnerID
	}

	switch event.EventType {
	case streampkg.EventStatusStarted, streampkg.EventStatusProgress, streampkg.EventStatusCompleted, streampkg.EventStatusFailed:
		var delta struct {
			RunnerID string `json:"runner_id"`
		}
		if err := json.Unmarshal(event.Delta, &delta); err == nil && delta.RunnerID != "" && v.RunnerID == "" {
			v.RunnerID = delta.RunnerID
		}
		if event.EventType == streampkg.EventStatusFailed {
			v.Status = runnerpkg.RunnerStatusFailed
		}
	case streampkg.EventRunnerPhaseChanged:
		var delta struct {
			Phase  string `json:"phase"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			return fmt.Errorf("decode runner phase delta: %w", err)
		}
		if delta.Phase != "" {
			v.Phase = runnerpkg.RunnerPhase(delta.Phase)
		}
		if delta.Status != "" {
			v.Status = runnerpkg.RunnerStatus(delta.Status)
		}
	case streampkg.EventRunnerNodeAdded:
		var delta struct {
			NodeID          string   `json:"node_id"`
			SkillName       string   `json:"skill_name"`
			TaskDescription string   `json:"task_description"`
			DependsOn       []string `json:"depends_on"`
		}
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			return fmt.Errorf("decode runner node-added delta: %w", err)
		}
		nodeID := firstNonEmpty(delta.NodeID, event.Scope.NodeID)
		if nodeID == "" {
			return fmt.Errorf("runner node-added event missing node_id")
		}
		node := v.Nodes[nodeID]
		node.ID = nodeID
		node.SkillName = firstNonEmpty(delta.SkillName, node.SkillName, metadataString(event.Metadata, "skill_name"))
		node.TaskDescription = firstNonEmpty(delta.TaskDescription, node.TaskDescription)
		node.DependsOn = cloneStringSlice(delta.DependsOn)
		node.Status = event.Status
		v.Nodes[nodeID] = node
	case streampkg.EventRunnerNodeStarted, streampkg.EventRunnerNodeCompleted, streampkg.EventRunnerNodeFailed:
		var delta struct {
			NodeID string `json:"node_id"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			return fmt.Errorf("decode runner node delta: %w", err)
		}
		nodeID := firstNonEmpty(delta.NodeID, event.Scope.NodeID)
		if nodeID == "" {
			return fmt.Errorf("runner node event missing node_id")
		}
		node := v.Nodes[nodeID]
		node.ID = nodeID
		node.Status = event.Status
		if event.EventType == streampkg.EventRunnerNodeFailed {
			node.Error = strings.TrimSpace(delta.Error)
		}
		if node.SkillName == "" {
			node.SkillName = metadataString(event.Metadata, "skill_name")
		}
		v.Nodes[nodeID] = node
	case streampkg.EventArtifactCreated:
		var delta struct {
			Path         string `json:"path"`
			ResourceType string `json:"resource_type"`
			Description  string `json:"description"`
			Stage        string `json:"stage"`
		}
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			return fmt.Errorf("decode artifact delta: %w", err)
		}
		if strings.TrimSpace(delta.Path) == "" {
			return fmt.Errorf("artifact event missing path")
		}
		artifact := DescribeArtifact{
			NodeID:       event.Scope.NodeID,
			Path:         delta.Path,
			ResourceType: delta.ResourceType,
			Description:  delta.Description,
			Stage:        delta.Stage,
		}
		v.upsertArtifact(artifact)
	}
	return nil
}

func (v *DescribeView) upsertArtifact(artifact DescribeArtifact) {
	for i := range v.Artifacts {
		if v.Artifacts[i].NodeID == artifact.NodeID && v.Artifacts[i].Path == artifact.Path {
			v.Artifacts[i] = artifact
			return
		}
	}
	v.Artifacts = append(v.Artifacts, artifact)
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}
