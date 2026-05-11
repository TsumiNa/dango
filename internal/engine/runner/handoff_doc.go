package runner

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/adrg/frontmatter"
	streampkg "github.com/tsumina/dango/internal/engine/stream"
	"gopkg.in/yaml.v2"
)

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
	streampkg.ChannelHeader `json:",inline" yaml:",inline"`
	FromNode                string            `json:"from_node" yaml:"from_node"`
	ToNodes                 []string          `json:"to_nodes" yaml:"to_nodes"`
	Intent                  string            `json:"intent,omitempty" yaml:"intent,omitempty"`
	Artifacts               []HandoffArtifact `json:"artifacts,omitempty" yaml:"artifacts,omitempty"`
	Body                    string            `json:"body,omitempty" yaml:"-"`
}

type handoffDocFrontMatter struct {
	streampkg.ChannelHeader `yaml:",inline"`
	FromNode                string            `yaml:"from_node"`
	ToNodes                 []string          `yaml:"to_nodes"`
	Intent                  string            `yaml:"intent,omitempty"`
	Artifacts               []HandoffArtifact `yaml:"artifacts,omitempty"`
}

// Markdown renders doc as a canonical handoff markdown document.
func (doc HandoffDoc) Markdown() (string, error) {
	if doc.Kind == "" {
		doc.Kind = streampkg.ChannelKindHandoff
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
		trimmed := strings.TrimSpace(to)
		if trimmed == "" {
			return "", fmt.Errorf("runner: handoff doc to_nodes must not contain empty values")
		}
		if trimmed != to {
			return "", fmt.Errorf("runner: handoff doc to_nodes must not contain leading or trailing whitespace")
		}
	}
	for _, artifact := range doc.Artifacts {
		if err := validateHandoffArtifactPath(artifact.Path); err != nil {
			return "", err
		}
	}
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now()
	}

	meta := handoffDocFrontMatter{
		ChannelHeader: streampkg.ChannelHeader{
			Kind:      doc.Kind,
			Version:   doc.Version,
			RunnerID:  doc.RunnerID,
			CreatedAt: doc.CreatedAt,
		},
		FromNode:  doc.FromNode,
		ToNodes:   doc.ToNodes,
		Intent:    doc.Intent,
		Artifacts: doc.Artifacts,
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
	if meta.Kind != streampkg.ChannelKindHandoff {
		return nil, fmt.Errorf("runner: handoff doc kind = %q, want %q", meta.Kind, streampkg.ChannelKindHandoff)
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
		trimmed := strings.TrimSpace(to)
		if trimmed == "" {
			return nil, fmt.Errorf("runner: handoff doc to_nodes must not contain empty values")
		}
		if trimmed != to {
			return nil, fmt.Errorf("runner: handoff doc to_nodes must not contain leading or trailing whitespace")
		}
	}
	for _, artifact := range meta.Artifacts {
		if err := validateHandoffArtifactPath(artifact.Path); err != nil {
			return nil, err
		}
	}
	if meta.CreatedAt.IsZero() {
		return nil, fmt.Errorf("runner: handoff doc created_at must not be empty")
	}
	return &HandoffDoc{
		ChannelHeader: streampkg.ChannelHeader{
			Kind:      meta.Kind,
			Version:   meta.Version,
			RunnerID:  meta.RunnerID,
			CreatedAt: meta.CreatedAt,
		},
		FromNode:  meta.FromNode,
		ToNodes:   meta.ToNodes,
		Intent:    meta.Intent,
		Artifacts: meta.Artifacts,
		Body:      strings.TrimSpace(string(body)),
	}, nil
}

// validateHandoffArtifactPath validates that artifact paths are non-empty,
// relative, and do not escape the workspace via parent traversal.
func validateHandoffArtifactPath(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("runner: handoff doc artifact path must not be empty")
	}
	if trimmed != raw {
		return fmt.Errorf("runner: handoff doc artifact path must not contain leading or trailing whitespace")
	}
	if path.IsAbs(trimmed) {
		return fmt.Errorf("runner: handoff doc artifact path must be relative: %q", trimmed)
	}
	clean := path.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("runner: handoff doc artifact path must not escape workspace: %q", trimmed)
	}
	return nil
}
