package runner

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

func (r *Runner) emitChannelDocumentEvents(ctx context.Context, node *Node, output any) {
	if r.eventStream == nil || node == nil {
		return
	}
	text, ok := output.(string)
	if !ok {
		return
	}
	if doc, err := ParseHandoffMarkdown(text); err == nil {
		r.emitHandoffEvents(ctx, node, doc)
		return
	}
	if doc, err := ParseExchangeDocMarkdown(text); err == nil {
		r.emitExchangePublishedEvent(ctx, node, doc)
	}
}

func (r *Runner) emitHandoffEvents(ctx context.Context, node *Node, doc *HandoffDoc) {
	payload := streampkg.HandoffEmittedPayload{
		RunnerID:  r.id,
		FromNode:  node.Id,
		ToNodes:   append([]string(nil), doc.ToNodes...),
		Intent:    doc.Intent,
		Document:  strings.TrimSpace(doc.Body),
		Artifacts: handoffArtifactPayloads(doc.Artifacts),
		CreatedAt: doc.CreatedAt,
	}
	if r.workspace != nil {
		if ws, ok := r.workspace.Skill(node.Id); ok {
			payload.Path = filepath.Join(ws.DownstreamDir, "handoff.md")
		}
	}
	r.emitSkillStreamEvent(ctx, streampkg.EventHandoffEmitted, streampkg.StatusCompleted, node.Id, node, payloadMap(payload))

	for _, artifact := range doc.Artifacts {
		declaredPath := strings.TrimSpace(artifact.Path)
		if declaredPath == "" {
			continue
		}
		artifactPath := declaredPath
		if resolved, ok := r.resolveNodeArtifactPath(node.Id, declaredPath); ok {
			artifactPath = resolved
		}
		delta := map[string]any{"path": artifactPath}
		if artifactPath != declaredPath {
			delta["declared_path"] = declaredPath
		}
		if artifact.Type != "" {
			delta["resource_type"] = artifact.Type
		}
		if artifact.Description != "" {
			delta["description"] = artifact.Description
		}
		if doc.Intent != "" {
			delta["intent"] = doc.Intent
		}
		r.emitExecutorStreamEvent(ctx, streampkg.EventArtifactCreated, streampkg.StatusCompleted, node.Id, node, delta)
	}
}

func (r *Runner) emitExchangePublishedEvent(ctx context.Context, node *Node, doc *ExchangeDoc) {
	payload := streampkg.ExchangePublishedPayload{
		RunnerID:  r.id,
		NodeID:    node.Id,
		Document:  strings.TrimSpace(doc.Body),
		Title:     doc.Title,
		CreatedAt: doc.CreatedAt,
	}
	if r.workspace != nil {
		payload.Path = r.workspace.ExchangeDir()
	}
	r.emitSkillStreamEvent(ctx, streampkg.EventExchangePublished, streampkg.StatusCompleted, node.Id, node, payloadMap(payload))
}

func (r *Runner) deliverHandoffToSuccessor(ctx context.Context, producer *Node, successor *Node) error {
	if r.workspace == nil || producer == nil || successor == nil {
		return nil
	}
	if err := r.workspace.Handoff(producer.Id, successor.Id); err != nil {
		return err
	}
	successorWS, ok := r.workspace.Skill(successor.Id)
	if !ok {
		return nil
	}
	inboxPath := filepath.Join(successorWS.UpstreamDir, producer.Id)
	payload := streampkg.HandoffDeliveredPayload{
		RunnerID:    r.id,
		FromNode:    producer.Id,
		ToNode:      successor.Id,
		InboxPath:   inboxPath,
		HandoffPath: filepath.Join(inboxPath, "handoff.md"),
		DeliveredAt: time.Now(),
	}
	r.emitStreamEvent(ctx, streampkg.EventHandoffDelivered, streampkg.StatusCompleted, payload, streampkg.Scope{RunnerID: r.id, NodeID: successor.Id}, map[string]any{
		"runner_id": r.id,
		"from_node": producer.Id,
		"to_node":   successor.Id,
	})
	return nil
}

func (r *Runner) resolveNodeArtifactPath(nodeID string, declaredPath string) (string, bool) {
	if r.workspace == nil {
		return "", false
	}
	workspace, ok := r.workspace.Skill(nodeID)
	if !ok {
		return "", false
	}
	return resolveHandoffArtifactPath(workspace, declaredPath)
}

func handoffArtifactPayloads(artifacts []HandoffArtifact) []streampkg.HandoffArtifactPayload {
	if len(artifacts) == 0 {
		return nil
	}
	out := make([]streampkg.HandoffArtifactPayload, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, streampkg.HandoffArtifactPayload{
			Path:        artifact.Path,
			Type:        artifact.Type,
			Description: artifact.Description,
		})
	}
	return out
}

func payloadMap(payload any) map[string]any {
	switch p := payload.(type) {
	case streampkg.HandoffEmittedPayload:
		return map[string]any{
			"runner_id":  p.RunnerID,
			"from_node":  p.FromNode,
			"to_nodes":   p.ToNodes,
			"intent":     p.Intent,
			"path":       p.Path,
			"document":   p.Document,
			"artifacts":  p.Artifacts,
			"created_at": p.CreatedAt,
		}
	case streampkg.ExchangePublishedPayload:
		return map[string]any{
			"runner_id":  p.RunnerID,
			"node_id":    p.NodeID,
			"path":       p.Path,
			"document":   p.Document,
			"title":      p.Title,
			"created_at": p.CreatedAt,
		}
	default:
		return map[string]any{}
	}
}
