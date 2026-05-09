package runner

import (
	"fmt"
	"strings"
	"time"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v2"
)

// HandoffDocKind is the front matter kind used by directed handoff documents.
const HandoffDocKind = "dango.handoff_doc"

// HandoffDocVersion is the schema version for [HandoffDoc] markdown.
const HandoffDocVersion = 1

// HandoffArtifact describes one artifact referenced by a [HandoffDoc].
type HandoffArtifact struct {
	Path        string `json:"path" yaml:"path"`
	Type        string `json:"type,omitempty" yaml:"type,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// HandoffDoc is a front-mattered markdown parcel routed to downstream skills.
type HandoffDoc struct {
	Kind      string            `json:"kind" yaml:"kind"`
	Version   int               `json:"version" yaml:"version"`
	RunnerID  string            `json:"runner_id" yaml:"runner_id"`
	FromNode  string            `json:"from_node" yaml:"from_node"`
	ToNodes   []string          `json:"to_nodes" yaml:"to_nodes"`
	Intent    string            `json:"intent,omitempty" yaml:"intent,omitempty"`
	CreatedAt time.Time         `json:"created_at" yaml:"created_at"`
	Artifacts []HandoffArtifact `json:"artifacts,omitempty" yaml:"artifacts,omitempty"`
	Body      string            `json:"body,omitempty" yaml:"-"`
}

type handoffDocFrontMatter struct {
	Kind      string            `yaml:"kind"`
	Version   int               `yaml:"version"`
	RunnerID  string            `yaml:"runner_id"`
	FromNode  string            `yaml:"from_node"`
	ToNodes   []string          `yaml:"to_nodes"`
	Intent    string            `yaml:"intent,omitempty"`
	CreatedAt time.Time         `yaml:"created_at"`
	Artifacts []HandoffArtifact `yaml:"artifacts,omitempty"`
}

// Markdown renders doc as a canonical handoff markdown document.
func (doc HandoffDoc) Markdown() (string, error) {
	if doc.Kind == "" {
		doc.Kind = HandoffDocKind
	}
	if doc.Version == 0 {
		doc.Version = HandoffDocVersion
	}
	if doc.RunnerID == "" {
		return "", fmt.Errorf("runner: handoff doc runner_id must not be empty")
	}
	if doc.FromNode == "" {
		return "", fmt.Errorf("runner: handoff doc from_node must not be empty")
	}
	if len(doc.ToNodes) == 0 {
		return "", fmt.Errorf("runner: handoff doc to_nodes must not be empty")
	}
	for _, to := range doc.ToNodes {
		if strings.TrimSpace(to) == "" {
			return "", fmt.Errorf("runner: handoff doc to_nodes must not contain empty values")
		}
	}
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now().UTC()
	}

	meta := handoffDocFrontMatter{
		Kind:      doc.Kind,
		Version:   doc.Version,
		RunnerID:  doc.RunnerID,
		FromNode:  doc.FromNode,
		ToNodes:   append([]string(nil), doc.ToNodes...),
		Intent:    doc.Intent,
		CreatedAt: doc.CreatedAt,
		Artifacts: append([]HandoffArtifact(nil), doc.Artifacts...),
	}
	front, err := yaml.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("runner: marshal handoff doc front matter: %w", err)
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.Write(front)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(doc.Body))
	b.WriteString("\n")
	return b.String(), nil
}

// ParseHandoffMarkdown parses raw as a handoff markdown document.
func ParseHandoffMarkdown(raw string) (*HandoffDoc, error) {
	var meta handoffDocFrontMatter
	body, err := frontmatter.Parse(strings.NewReader(raw), &meta)
	if err != nil {
		return nil, fmt.Errorf("runner: parse handoff doc front matter: %w", err)
	}
	if meta.Kind != HandoffDocKind {
		return nil, fmt.Errorf("runner: handoff doc kind = %q, want %q", meta.Kind, HandoffDocKind)
	}
	if meta.Version != HandoffDocVersion {
		return nil, fmt.Errorf("runner: handoff doc version = %d, want %d", meta.Version, HandoffDocVersion)
	}
	if meta.RunnerID == "" {
		return nil, fmt.Errorf("runner: handoff doc runner_id must not be empty")
	}
	if meta.FromNode == "" {
		return nil, fmt.Errorf("runner: handoff doc from_node must not be empty")
	}
	if len(meta.ToNodes) == 0 {
		return nil, fmt.Errorf("runner: handoff doc to_nodes must not be empty")
	}
	for _, to := range meta.ToNodes {
		if strings.TrimSpace(to) == "" {
			return nil, fmt.Errorf("runner: handoff doc to_nodes must not contain empty values")
		}
	}
	if meta.CreatedAt.IsZero() {
		return nil, fmt.Errorf("runner: handoff doc created_at must not be empty")
	}
	return &HandoffDoc{
		Kind:      meta.Kind,
		Version:   meta.Version,
		RunnerID:  meta.RunnerID,
		FromNode:  meta.FromNode,
		ToNodes:   append([]string(nil), meta.ToNodes...),
		Intent:    meta.Intent,
		CreatedAt: meta.CreatedAt,
		Artifacts: append([]HandoffArtifact(nil), meta.Artifacts...),
		Body:      strings.TrimSpace(string(body)),
	}, nil
}
