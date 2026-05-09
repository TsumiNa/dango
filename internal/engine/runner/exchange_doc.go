package runner

import (
	"fmt"
	"strings"
	"time"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v2"
)

// ExchangeDocKind is the front matter kind used by exchange entry documents.
const ExchangeDocKind = "dango.exchange_doc"

// ExchangeDocVersion is the schema version for [ExchangeDoc] markdown.
const ExchangeDocVersion = 1

// ExchangeDoc is a front-mattered markdown entry for the shared exchange space.
type ExchangeDoc struct {
	Kind      string    `json:"kind" yaml:"kind"`
	Version   int       `json:"version" yaml:"version"`
	RunnerID  string    `json:"runner_id" yaml:"runner_id"`
	NodeID    string    `json:"node_id" yaml:"node_id"`
	SkillName string    `json:"skill_name,omitempty" yaml:"skill_name,omitempty"`
	Title     string    `json:"title,omitempty" yaml:"title,omitempty"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	Body      string    `json:"body,omitempty" yaml:"-"`
}

type exchangeDocFrontMatter struct {
	Kind      string    `yaml:"kind"`
	Version   int       `yaml:"version"`
	RunnerID  string    `yaml:"runner_id"`
	NodeID    string    `yaml:"node_id"`
	SkillName string    `yaml:"skill_name,omitempty"`
	Title     string    `yaml:"title,omitempty"`
	CreatedAt time.Time `yaml:"created_at"`
}

// Markdown renders doc as a canonical exchange-entry markdown document.
func (doc ExchangeDoc) Markdown() (string, error) {
	if doc.Kind == "" {
		doc.Kind = ExchangeDocKind
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
		Kind:      doc.Kind,
		Version:   doc.Version,
		RunnerID:  doc.RunnerID,
		NodeID:    doc.NodeID,
		SkillName: doc.SkillName,
		Title:     doc.Title,
		CreatedAt: doc.CreatedAt,
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
	if meta.Kind != ExchangeDocKind {
		return nil, fmt.Errorf("runner: exchange doc kind = %q, want %q", meta.Kind, ExchangeDocKind)
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
		Kind:      meta.Kind,
		Version:   meta.Version,
		RunnerID:  meta.RunnerID,
		NodeID:    meta.NodeID,
		SkillName: meta.SkillName,
		Title:     meta.Title,
		CreatedAt: meta.CreatedAt,
		Body:      strings.TrimSpace(string(body)),
	}, nil
}
