package runner

import (
	"fmt"
	"strings"
	"time"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v2"
)

// MemoDocumentKind is the front matter kind used by memo snapshot documents.
const MemoDocumentKind = "dango.memo"

// MemoDocumentVersion is the schema version for [MemoDocument] markdown.
const MemoDocumentVersion = 1

// MemoDocument is a front-mattered markdown snapshot of one skill memo file.
type MemoDocument struct {
	Kind      string    `json:"kind" yaml:"kind"`
	Version   int       `json:"version" yaml:"version"`
	RunnerID  string    `json:"runner_id" yaml:"runner_id"`
	NodeID    string    `json:"node_id" yaml:"node_id"`
	SkillName string    `json:"skill_name,omitempty" yaml:"skill_name,omitempty"`
	Path      string    `json:"path" yaml:"path"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	Body      string    `json:"body,omitempty" yaml:"-"`
}

type memoFrontMatter struct {
	Kind      string    `yaml:"kind"`
	Version   int       `yaml:"version"`
	RunnerID  string    `yaml:"runner_id"`
	NodeID    string    `yaml:"node_id"`
	SkillName string    `yaml:"skill_name,omitempty"`
	Path      string    `yaml:"path"`
	CreatedAt time.Time `yaml:"created_at"`
}

// Markdown renders doc as a canonical memo markdown document.
func (doc MemoDocument) Markdown() (string, error) {
	if doc.Kind == "" {
		doc.Kind = MemoDocumentKind
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
		Kind:      doc.Kind,
		Version:   doc.Version,
		RunnerID:  doc.RunnerID,
		NodeID:    doc.NodeID,
		SkillName: doc.SkillName,
		Path:      doc.Path,
		CreatedAt: doc.CreatedAt,
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
	if meta.Kind != MemoDocumentKind {
		return nil, fmt.Errorf("runner: memo document kind = %q, want %q", meta.Kind, MemoDocumentKind)
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
		Kind:      meta.Kind,
		Version:   meta.Version,
		RunnerID:  meta.RunnerID,
		NodeID:    meta.NodeID,
		SkillName: meta.SkillName,
		Path:      meta.Path,
		CreatedAt: meta.CreatedAt,
		Body:      strings.TrimSpace(string(body)),
	}, nil
}
