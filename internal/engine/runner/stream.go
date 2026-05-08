package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

const exchangeMemoStreamTextLimit = 4096

// SubscribeStream attaches a subscriber to the runner's structured event
// stream. Replay and filtering are handled by the stream package.
func (r *Runner) SubscribeStream(filter streampkg.Filter, opts ...streampkg.SubscribeOption) (*streampkg.Subscription, error) {
	return r.eventStream.Subscribe(filter, opts...)
}

// emitPhaseChangedEvent announces that the runner's phase has changed. It
// emits a runner.phase.changed event for internal coordination and external
// subscribers.
//
// All phase transitions must call this so that direct phase assignments stay
// visible to replaying stream subscribers.
func (r *Runner) emitPhaseChangedEvent() {
	if r.eventStream == nil {
		return
	}
	state := r.State()
	phase := r.Phase()
	r.emitStreamEvent(context.Background(),
		streampkg.EventRunnerPhaseChanged,
		runnerStreamStatus(state.Status),
		map[string]any{
			"phase":  phase,
			"status": state.Status,
		},
		streampkg.Scope{RunnerID: r.id},
		nil,
	)
}

// emitNodeStreamEvent emits the structured stream event that corresponds to an
// internal RunnerEvent from the engine loop.
func (r *Runner) emitNodeStreamEvent(event *RunnerEvent) {
	if r.eventStream == nil {
		return
	}
	if event == nil {
		return
	}
	state := r.State()
	eventType, status, ok := runnerEventStreamType(event.Type, state.Status)
	if !ok {
		return
	}
	r.updateMu.Lock()
	node := r.snapshot.NodesData[event.NodeID]
	r.updateMu.Unlock()
	delta := map[string]any{
		"event": event.Type.String(),
	}
	scope := streampkg.Scope{RunnerID: r.id}
	if event.NodeID != "" {
		scope.NodeID = event.NodeID
		delta["node_id"] = event.NodeID
	}
	if event.Type == EventNodeAdded && node != nil {
		if node.SkillName != "" {
			delta["skill_name"] = node.SkillName
		}
		if node.TaskDescription != "" {
			delta["task_description"] = node.TaskDescription
		}
		if len(node.Parents) > 0 {
			dependsOn := make([]string, 0, len(node.Parents))
			for _, parent := range node.Parents {
				if parent != nil && parent.Id != "" {
					dependsOn = append(dependsOn, parent.Id)
				}
			}
			if len(dependsOn) > 0 {
				delta["depends_on"] = dependsOn
			}
		}
	}
	if event.Type == EventNodeFailed && event.Data != nil {
		delta["error"] = compactStreamText(fmt.Sprint(event.Data))
	}
	metadata := r.nodeEventMetadata(event)
	r.emitStreamEvent(context.Background(), eventType, status, delta, scope, metadata)
}

func runnerEventStreamType(eventType EventType, status RunnerStatus) (string, string, bool) {
	switch eventType {
	case EventNodeAdded:
		return streampkg.EventRunnerNodeAdded, streampkg.StatusPending, true
	case EventNodeStarted:
		return streampkg.EventRunnerNodeStarted, streampkg.StatusRunning, true
	case EventNodeCompleted:
		return streampkg.EventRunnerNodeCompleted, streampkg.StatusCompleted, true
	case EventNodeFailed:
		return streampkg.EventRunnerNodeFailed, streampkg.StatusFailed, true
	case EventEngineIdle:
		return streampkg.EventStatusProgress, streampkg.StatusCompleted, true
	case EventEngineStopped:
		if status == RunnerStatusFailed {
			return streampkg.EventStatusProgress, streampkg.StatusFailed, true
		}
		return streampkg.EventStatusProgress, streampkg.StatusCompleted, true
	default:
		return "", "", false
	}
}

func (r *Runner) nodeEventMetadata(event *RunnerEvent) map[string]any {
	phase := r.Phase()
	metadata := map[string]any{
		"runner_id": r.id,
		"phase":     phase,
	}
	if event == nil || event.NodeID == "" {
		return metadata
	}
	metadata["node_id"] = event.NodeID
	r.updateMu.Lock()
	node := r.snapshot.NodesData[event.NodeID]
	r.updateMu.Unlock()
	if node != nil && node.SkillName != "" {
		metadata["skill_name"] = node.SkillName
	}
	return metadata
}

func (r *Runner) emitStreamEvent(ctx context.Context, eventType string, status string, delta any, scope streampkg.Scope, metadata map[string]any) {
	r.emitStreamEventFrom(ctx,
		streampkg.Source{Layer: "runner", ID: r.id},
		eventType,
		status,
		delta,
		scope,
		metadata,
	)
}

func (r *Runner) emitExecutorStreamEvent(ctx context.Context, eventType string, status string, nodeID string, node *Node, delta map[string]any) {
	if r.eventStream == nil {
		return
	}
	if delta == nil {
		delta = map[string]any{}
	}
	delta["node_id"] = nodeID
	metadata := map[string]any{
		"runner_id": r.id,
		"node_id":   nodeID,
	}
	if node != nil && node.SkillName != "" {
		metadata["skill_name"] = node.SkillName
	}
	r.emitStreamEventFrom(ctx,
		streampkg.Source{Layer: "executor", ID: nodeID, ParentID: r.id},
		eventType,
		status,
		delta,
		streampkg.Scope{RunnerID: r.id, NodeID: nodeID},
		metadata,
	)
}

func (r *Runner) emitSkillStreamEvent(ctx context.Context, eventType string, status string, nodeID string, node *Node, delta map[string]any) {
	if r.eventStream == nil {
		return
	}
	if delta == nil {
		delta = map[string]any{}
	}
	delta["node_id"] = nodeID
	metadata := map[string]any{
		"runner_id": r.id,
		"node_id":   nodeID,
	}
	source := streampkg.Source{Layer: "skill", ParentID: nodeID}
	if node != nil && node.SkillName != "" {
		source.ID = node.SkillName
		metadata["skill_name"] = node.SkillName
	}
	r.emitStreamEventFrom(ctx,
		source,
		eventType,
		status,
		delta,
		streampkg.Scope{RunnerID: r.id, NodeID: nodeID},
		metadata,
	)
}

func (r *Runner) emitExchangeDocumentEvents(ctx context.Context, node *Node, output any) {
	if r.eventStream == nil || node == nil {
		return
	}
	text, ok := output.(string)
	if !ok {
		return
	}
	doc, err := ParseExchangeMarkdown(text)
	if err != nil {
		return
	}
	if memo := strings.TrimSpace(doc.Memo); memo != "" {
		truncated := false
		if len(memo) > exchangeMemoStreamTextLimit {
			memo = memo[:exchangeMemoStreamTextLimit]
			truncated = true
		}
		delta := map[string]any{"memo": memo}
		if doc.Stage != "" {
			delta["stage"] = doc.Stage
		}
		if truncated {
			delta["truncated"] = true
		}
		r.emitSkillStreamEvent(ctx, streampkg.EventSkillMemoDelta, streampkg.StatusCompleted, node.Id, node, delta)
	}

	for _, resource := range doc.Resources {
		path := strings.TrimSpace(resource.Path)
		if path == "" {
			continue
		}
		delta := map[string]any{"path": path}
		if resource.Type != "" {
			delta["resource_type"] = resource.Type
		}
		if resource.Description != "" {
			delta["description"] = resource.Description
		}
		if doc.Stage != "" {
			delta["stage"] = doc.Stage
		}
		r.emitExecutorStreamEvent(ctx, streampkg.EventArtifactCreated, streampkg.StatusCompleted, node.Id, node, delta)
	}
}

func (r *Runner) emitStreamEventFrom(ctx context.Context, source streampkg.Source, eventType string, status string, delta any, scope streampkg.Scope, metadata map[string]any) {
	raw, err := json.Marshal(delta)
	if err != nil {
		raw, _ = json.Marshal(fmt.Sprint(delta))
	}
	if err := r.eventStream.Emit(ctx, streampkg.Event{
		EventType: eventType,
		From:      source,
		Status:    status,
		Delta:     json.RawMessage(raw),
		Scope:     scope,
		Metadata:  metadata,
	}); err != nil {
		r.logger.Debug("stream event emit failed", "runner_id", r.id, "event_type", eventType, "err", err)
	}
}

func runnerStreamStatus(status RunnerStatus) string {
	switch status {
	case RunnerStatusPending:
		return streampkg.StatusPending
	case RunnerStatusRunning:
		return streampkg.StatusRunning
	case RunnerStatusIdle:
		return streampkg.StatusCompleted
	case RunnerStatusFailed, RunnerStatusCanceled:
		return streampkg.StatusFailed
	default:
		return streampkg.StatusRunning
	}
}

func compactStreamText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 240 {
		text = text[:240] + "..."
	}
	return text
}
