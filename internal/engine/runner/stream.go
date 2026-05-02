package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

func (r *Runner) publishStreamUpdate(update RunnerUpdate) {
	if r.eventStream == nil {
		return
	}
	if update.Event == nil {
		r.emitStreamEvent(context.Background(),
			streampkg.EventRunnerPhaseChanged,
			runnerStreamStatus(update.State.Status),
			map[string]any{
				"phase":  update.Phase,
				"status": update.State.Status,
			},
			streampkg.Scope{RunnerID: r.id},
			nil,
		)
		return
	}

	eventType, status, ok := runnerEventStreamType(update)
	if !ok {
		return
	}
	delta := map[string]any{
		"event": update.Event.Type.String(),
	}
	scope := streampkg.Scope{RunnerID: r.id}
	if update.Event.NodeID != "" {
		scope.NodeID = update.Event.NodeID
		delta["node_id"] = update.Event.NodeID
	}
	if update.Event.Type == EventNodeFailed && update.Event.Data != nil {
		delta["error"] = compactStreamText(fmt.Sprint(update.Event.Data))
	}
	metadata := runnerEventMetadata(update)
	r.emitStreamEvent(context.Background(), eventType, status, delta, scope, metadata)
}

func runnerEventStreamType(update RunnerUpdate) (string, string, bool) {
	switch update.Event.Type {
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
		return streampkg.EventStatusProgress, runnerStreamStatus(update.State.Status), true
	default:
		return "", "", false
	}
}

func runnerEventMetadata(update RunnerUpdate) map[string]any {
	metadata := map[string]any{
		"runner_id": update.RunnerID,
		"phase":     update.Phase,
	}
	if update.Event == nil || update.Event.NodeID == "" {
		return metadata
	}
	metadata["node_id"] = update.Event.NodeID
	if node := update.Snapshot.NodesData[update.Event.NodeID]; node != nil {
		if node.SkillName != "" {
			metadata["skill_name"] = node.SkillName
		}
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
