package runner

import (
	"fmt"
	"strings"
	"time"

	"github.com/adrg/frontmatter"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"gopkg.in/yaml.v2"
)

// ExchangeDocVersion is the schema version for [ExchangeDoc] markdown.
const ExchangeDocVersion = 1

// ExchangeDoc is a front-mattered markdown entry for the shared exchange space.
type ExchangeDoc struct {
	streampkg.ChannelHeader `json:",inline" yaml:",inline"`
	NodeID                  string `json:"node_id" yaml:"node_id"`
	SkillName               string `json:"skill_name,omitempty" yaml:"skill_name,omitempty"`
	Title                   string `json:"title,omitempty" yaml:"title,omitempty"`
	Body                    string `json:"body,omitempty" yaml:"-"`
}

type exchangeDocFrontMatter struct {
	streampkg.ChannelHeader `yaml:",inline"`
	NodeID                  string `yaml:"node_id"`
	SkillName               string `yaml:"skill_name,omitempty"`
	Title                   string `yaml:"title,omitempty"`
}

// Markdown renders doc as a canonical exchange-entry markdown document.
func (doc ExchangeDoc) Markdown() (string, error) {
	if doc.Kind == "" {
		doc.Kind = streampkg.ChannelKindExchange
	}
	if doc.Version == 0 {
		doc.Version = ExchangeDocVersion
	}
	if doc.RunnerID == "" {
		return "", fmt.Errorf("runner: exchange doc runner_id must not be empty")
	}
	if doc.NodeID == "" {
		return "", fmt.Errorf("runner: exchange doc node_id must not be empty")
	}
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now()
	}

	meta := exchangeDocFrontMatter{
		ChannelHeader: streampkg.ChannelHeader{
			Kind:      doc.Kind,
			Version:   doc.Version,
			RunnerID:  doc.RunnerID,
			CreatedAt: doc.CreatedAt,
		},
		NodeID:    doc.NodeID,
		SkillName: doc.SkillName,
		Title:     doc.Title,
	}
	front, err := yaml.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("runner: marshal exchange doc front matter: %w", err)
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.Write(front)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(doc.Body))
	b.WriteString("\n")
	return b.String(), nil
}

// ParseExchangeDocMarkdown parses raw as an exchange-entry markdown document.
func ParseExchangeDocMarkdown(raw string) (*ExchangeDoc, error) {
	var meta exchangeDocFrontMatter
	body, err := frontmatter.Parse(strings.NewReader(raw), &meta)
	if err != nil {
		return nil, fmt.Errorf("runner: parse exchange doc front matter: %w", err)
	}
	return buildExchangeDoc(meta, strings.TrimSpace(string(body)))
}

func buildExchangeDoc(meta exchangeDocFrontMatter, body string) (*ExchangeDoc, error) {
	if meta.Kind != streampkg.ChannelKindExchange {
		return nil, fmt.Errorf("runner: exchange doc kind = %q, want %q", meta.Kind, streampkg.ChannelKindExchange)
	}
	if meta.Version != ExchangeDocVersion {
		return nil, fmt.Errorf("runner: exchange doc version = %d, want %d", meta.Version, ExchangeDocVersion)
	}
	if meta.RunnerID == "" {
		return nil, fmt.Errorf("runner: exchange doc runner_id must not be empty")
	}
	if meta.NodeID == "" {
		return nil, fmt.Errorf("runner: exchange doc node_id must not be empty")
	}
	if meta.CreatedAt.IsZero() {
		return nil, fmt.Errorf("runner: exchange doc created_at must not be empty")
	}
	return &ExchangeDoc{
		ChannelHeader: streampkg.ChannelHeader{
			Kind:      meta.Kind,
			Version:   meta.Version,
			RunnerID:  meta.RunnerID,
			CreatedAt: meta.CreatedAt,
		},
		NodeID:    meta.NodeID,
		SkillName: meta.SkillName,
		Title:     meta.Title,
		Body:      body,
	}, nil
}
