package runner

import (
	"fmt"
	"strings"
	"time"

	"github.com/adrg/frontmatter"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"gopkg.in/yaml.v2"
)

// MemoDocumentVersion is the schema version for [MemoDocument] markdown.
const MemoDocumentVersion = 1

// MemoDocument is a front-mattered markdown snapshot of one skill memo file.
type MemoDocument struct {
	streampkg.ChannelHeader `json:",inline" yaml:",inline"`
	NodeID                  string `json:"node_id" yaml:"node_id"`
	SkillName               string `json:"skill_name,omitempty" yaml:"skill_name,omitempty"`
	Path                    string `json:"path" yaml:"path"`
	Body                    string `json:"body,omitempty" yaml:"-"`
}

type memoFrontMatter struct {
	streampkg.ChannelHeader `yaml:",inline"`
	NodeID                  string `yaml:"node_id"`
	SkillName               string `yaml:"skill_name,omitempty"`
	Path                    string `yaml:"path"`
}

// Markdown renders doc as a canonical memo markdown document.
func (doc MemoDocument) Markdown() (string, error) {
	if doc.Kind == "" {
		doc.Kind = streampkg.ChannelKindMemo
	}
	if doc.Version == 0 {
		doc.Version = MemoDocumentVersion
	}
	if doc.RunnerID == "" {
		return "", fmt.Errorf("runner: memo document runner_id must not be empty")
	}
	if doc.NodeID == "" {
		return "", fmt.Errorf("runner: memo document node_id must not be empty")
	}
	if doc.Path == "" {
		return "", fmt.Errorf("runner: memo document path must not be empty")
	}
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now()
	}

	meta := memoFrontMatter{
		ChannelHeader: streampkg.ChannelHeader{
			Kind:      doc.Kind,
			Version:   doc.Version,
			RunnerID:  doc.RunnerID,
			CreatedAt: doc.CreatedAt,
		},
		NodeID:    doc.NodeID,
		SkillName: doc.SkillName,
		Path:      doc.Path,
	}
	front, err := yaml.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("runner: marshal memo front matter: %w", err)
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.Write(front)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(doc.Body))
	b.WriteString("\n")
	return b.String(), nil
}

// ParseMemoMarkdown parses raw as a memo markdown document.
func ParseMemoMarkdown(raw string) (*MemoDocument, error) {
	var meta memoFrontMatter
	body, err := frontmatter.Parse(strings.NewReader(raw), &meta)
	if err != nil {
		return nil, fmt.Errorf("runner: parse memo front matter: %w", err)
	}
	if meta.Kind != streampkg.ChannelKindMemo {
		return nil, fmt.Errorf("runner: memo document kind = %q, want %q", meta.Kind, streampkg.ChannelKindMemo)
	}
	if meta.Version != MemoDocumentVersion {
		return nil, fmt.Errorf("runner: memo document version = %d, want %d", meta.Version, MemoDocumentVersion)
	}
	if meta.RunnerID == "" {
		return nil, fmt.Errorf("runner: memo document runner_id must not be empty")
	}
	if meta.NodeID == "" {
		return nil, fmt.Errorf("runner: memo document node_id must not be empty")
	}
	if meta.Path == "" {
		return nil, fmt.Errorf("runner: memo document path must not be empty")
	}
	if meta.CreatedAt.IsZero() {
		return nil, fmt.Errorf("runner: memo document created_at must not be empty")
	}
	return &MemoDocument{
		ChannelHeader: streampkg.ChannelHeader{
			Kind:      meta.Kind,
			Version:   meta.Version,
			RunnerID:  meta.RunnerID,
			CreatedAt: meta.CreatedAt,
		},
		NodeID:    meta.NodeID,
		SkillName: meta.SkillName,
		Path:      meta.Path,
		Body:      strings.TrimSpace(string(body)),
	}, nil
}
