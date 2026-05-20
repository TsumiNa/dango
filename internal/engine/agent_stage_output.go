package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

func (e *Agent) renderStageOutputs(stage string, intent string, toNodes []string, body string) (string, error) {
	paths := e.currentRuntimePaths()
	runnerID := paths.RunnerID
	nodeID := paths.NodeID
	skillName := paths.SkillName
	doc := runnerpkg.HandoffDoc{
		ChannelHeader: streampkg.ChannelHeader{
			RunnerID:  runnerID,
			CreatedAt: time.Now(),
		},
		FromNode: nodeID,
		ToNodes:  append([]string(nil), toNodes...),
		Intent:   intent,
		Body:     strings.TrimSpace(body),
	}
	handoffMarkdown, err := doc.Markdown()
	if err != nil {
		return "", err
	}
	if paths.DownstreamDir != "" {
		if err := os.MkdirAll(paths.DownstreamDir, 0o755); err != nil {
			return "", fmt.Errorf("orchestrate: create downstream dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(paths.DownstreamDir, "handoff.md"), []byte(handoffMarkdown), 0o644); err != nil {
			return "", fmt.Errorf("orchestrate: write handoff markdown: %w", err)
		}
	}
	if paths.ExchangeDir != "" {
		if err := os.MkdirAll(paths.ExchangeDir, 0o755); err != nil {
			return "", fmt.Errorf("orchestrate: create exchange dir: %w", err)
		}
		fileName := fmt.Sprintf("%s-%s-%d.md", stage, nodeID, time.Now().UnixNano())
		exchangeMarkdown, exchangeErr := e.exchangeDocMarkdown(runnerID, nodeID, skillName, stage, body)
		if exchangeErr != nil {
			return "", exchangeErr
		}
		if err := os.WriteFile(filepath.Join(paths.ExchangeDir, fileName), []byte(exchangeMarkdown), 0o644); err != nil {
			return "", fmt.Errorf("orchestrate: write exchange markdown: %w", err)
		}
	}
	if err := e.snapshotMemos(stage, paths); err != nil {
		return "", err
	}
	return handoffMarkdown, nil
}

func (e *Agent) exchangeDocMarkdown(runnerID string, nodeID string, skillName string, stage string, body string) (string, error) {
	exchange := runnerpkg.ExchangeDoc{
		ChannelHeader: streampkg.ChannelHeader{
			RunnerID:  runnerID,
			CreatedAt: time.Now(),
		},
		NodeID:    nodeID,
		SkillName: skillName,
		Title:     stage,
		Body:      strings.TrimSpace(body),
	}
	return exchange.Markdown()
}

func formatParentOutputs(parentOutputs map[string]any) string {
	if len(parentOutputs) == 0 {
		return "No parent handoffs."
	}
	keys := make([]string, 0, len(parentOutputs))
	for key := range parentOutputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString("### ")
		b.WriteString(key)
		b.WriteString("\n\n")
		b.WriteString(formatAny(parentOutputs[key]))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func formatAny(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		buf, err := json.MarshalIndent(value, "", "  ")
		if err == nil {
			return string(buf)
		}
		return fmt.Sprintf("%v", value)
	}
}
