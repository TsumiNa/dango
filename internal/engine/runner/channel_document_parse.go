package runner

import (
	"fmt"
	"strings"

	"github.com/adrg/frontmatter"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

type parsedChannelDocument struct {
	kind     streampkg.ChannelKind
	handoff  *HandoffDoc
	exchange *ExchangeDoc
	memo     *MemoDocument
}

// channelDocumentFrontMatter captures the shared runner channel header plus the
// union of kind-specific fields so the runner can parse front matter once and
// route validation by kind.
type channelDocumentFrontMatter struct {
	streampkg.ChannelHeader `yaml:",inline"`
	FromNode                string            `yaml:"from_node"`
	ToNodes                 []string          `yaml:"to_nodes"`
	Intent                  string            `yaml:"intent,omitempty"`
	Artifacts               []HandoffArtifact `yaml:"artifacts,omitempty"`
	NodeID                  string            `yaml:"node_id"`
	SkillName               string            `yaml:"skill_name,omitempty"`
	Title                   string            `yaml:"title,omitempty"`
	Path                    string            `yaml:"path"`
}

func parseChannelDocument(raw string) (*parsedChannelDocument, error) {
	var meta channelDocumentFrontMatter
	body, err := frontmatter.Parse(strings.NewReader(raw), &meta)
	if err != nil {
		return nil, fmt.Errorf("runner: parse channel doc front matter: %w", err)
	}

	trimmedBody := strings.TrimSpace(string(body))
	parsed := &parsedChannelDocument{kind: meta.Kind}
	switch meta.Kind {
	case streampkg.ChannelKindHandoff:
		parsed.handoff, err = buildHandoffDoc(handoffDocFrontMatter{
			ChannelHeader: meta.ChannelHeader,
			FromNode:      meta.FromNode,
			ToNodes:       meta.ToNodes,
			Intent:        meta.Intent,
			Artifacts:     meta.Artifacts,
		}, trimmedBody)
	case streampkg.ChannelKindExchange:
		parsed.exchange, err = buildExchangeDoc(exchangeDocFrontMatter{
			ChannelHeader: meta.ChannelHeader,
			NodeID:        meta.NodeID,
			SkillName:     meta.SkillName,
			Title:         meta.Title,
		}, trimmedBody)
	case streampkg.ChannelKindMemo:
		parsed.memo, err = buildMemoDocument(memoFrontMatter{
			ChannelHeader: meta.ChannelHeader,
			NodeID:        meta.NodeID,
			SkillName:     meta.SkillName,
			Path:          meta.Path,
		}, trimmedBody)
	default:
		return nil, fmt.Errorf("runner: channel document kind = %q, want handoff, exchange, or memo", meta.Kind)
	}
	if err != nil {
		return nil, err
	}
	return parsed, nil
}
