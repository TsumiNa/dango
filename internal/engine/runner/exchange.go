package runner

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/adrg/frontmatter"
	"gopkg.in/yaml.v2"
)

// ExchangeDocumentKind is the front matter kind used by Dango markdown
// exchange documents.
const ExchangeDocumentKind = "dango.exchange"

// ExchangeDocumentVersion is the current markdown exchange document schema
// version.
const ExchangeDocumentVersion = 1

// ExchangeStage identifies which runner lifecycle stage produced an
// [ExchangeDocument].
type ExchangeStage string

const (
	// ExchangeStagePolish is produced while an executor previews or refines
	// its assigned task before orchestrator review.
	ExchangeStagePolish ExchangeStage = "polish"
	// ExchangeStageExecute is produced by an executor's main task run.
	ExchangeStageExecute ExchangeStage = "execute"
	// ExchangeStageReport is produced after successful execution to summarize
	// the executor output for final orchestration.
	ExchangeStageReport ExchangeStage = "report"
)

const (
	// ExchangeRecipientOrchestrator routes a handoff to the orchestrator
	// skill for review, approval, or graph changes.
	ExchangeRecipientOrchestrator = "orchestrator"
	// ExchangeRecipientDownstream routes a handoff to dependent downstream
	// skills in the runner graph.
	ExchangeRecipientDownstream = "downstream"
)

const (
	// ExchangeIntentReview asks the orchestrator to review the document.
	ExchangeIntentReview = "review"
	// ExchangeIntentRerunPrevious asks the orchestrator to evaluate whether a
	// previous executor should be rerun with revised parameters.
	ExchangeIntentRerunPrevious = "rerun_previous"
	// ExchangeIntentContinue passes execution output to downstream skills.
	ExchangeIntentContinue = "continue"
	// ExchangeIntentSummarize passes a report summary to the orchestrator.
	ExchangeIntentSummarize = "summarize"
)

// ExchangeHandoff describes one intended recipient of an exchange document.
//
// The detailed human-readable content lives in the document's Handoff section;
// this metadata keeps routing, intent, and short summaries machine-readable.
type ExchangeHandoff struct {
	To      string `json:"to" yaml:"to"`
	Intent  string `json:"intent,omitempty" yaml:"intent,omitempty"`
	Summary string `json:"summary,omitempty" yaml:"summary,omitempty"`
}

// ExchangeResource describes a file or directory produced by an executor.
//
// Runner parses these front matter entries to make resource directories
// available to downstream executors without needing model reasoning in the
// runner itself.
type ExchangeResource struct {
	Path        string `json:"path" yaml:"path"`
	Type        string `json:"type,omitempty" yaml:"type,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// ExchangeDocument is the markdown envelope executors use to hand information
// to the orchestrator and to downstream skills.
//
// Metadata is stored as front matter so the document can be indexed in JSON,
// SQL, or files. The body is split into Memo, Reasoning, and Handoff sections
// so long-running state, reasoning traces, and recipient-facing output remain
// readable to both humans and skills.
type ExchangeDocument struct {
	Kind            string             `json:"kind" yaml:"kind"`
	Version         int                `json:"version" yaml:"version"`
	Stage           ExchangeStage      `json:"stage" yaml:"stage"`
	RunnerID        string             `json:"runner_id,omitempty" yaml:"runner_id,omitempty"`
	NodeID          string             `json:"node_id,omitempty" yaml:"node_id,omitempty"`
	SkillName       string             `json:"skill_name,omitempty" yaml:"skill_name,omitempty"`
	TaskDescription string             `json:"task_description,omitempty" yaml:"task_description,omitempty"`
	CreatedAt       time.Time          `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	Handoffs        []ExchangeHandoff  `json:"handoffs,omitempty" yaml:"handoffs,omitempty"`
	Resources       []ExchangeResource `json:"resources,omitempty" yaml:"resources,omitempty"`

	Memo      string `json:"memo,omitempty" yaml:"-"`
	Reasoning string `json:"reasoning,omitempty" yaml:"-"`
	Handoff   string `json:"handoff,omitempty" yaml:"-"`
}

// Markdown renders doc as a front matter markdown exchange document.
func (doc ExchangeDocument) Markdown() (string, error) {
	doc = withExchangeDefaults(doc, ExchangeDocument{})
	front, err := yaml.Marshal(exchangeFrontMatter{
		Kind:            doc.Kind,
		Version:         doc.Version,
		Stage:           doc.Stage,
		RunnerID:        doc.RunnerID,
		NodeID:          doc.NodeID,
		SkillName:       doc.SkillName,
		TaskDescription: doc.TaskDescription,
		CreatedAt:       doc.CreatedAt,
		Handoffs:        doc.Handoffs,
		Resources:       doc.Resources,
	})
	if err != nil {
		return "", fmt.Errorf("runner: marshal exchange front matter: %w", err)
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.Write(front)
	b.WriteString("---\n")
	writeExchangeSection(&b, "Memo", doc.Memo)
	writeExchangeSection(&b, "Reasoning", doc.Reasoning)
	writeExchangeSection(&b, "Handoff", doc.Handoff)
	return b.String(), nil
}

// ParseExchangeMarkdown parses raw as a front matter markdown exchange
// document.
func ParseExchangeMarkdown(raw string) (*ExchangeDocument, error) {
	meta, sections, err := parseExchangeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if meta.Kind != ExchangeDocumentKind {
		return nil, fmt.Errorf("runner: exchange document kind = %q, want %q", meta.Kind, ExchangeDocumentKind)
	}
	if meta.Version != ExchangeDocumentVersion {
		return nil, fmt.Errorf("runner: exchange document version = %d, want %d", meta.Version, ExchangeDocumentVersion)
	}
	if meta.Stage == "" {
		return nil, fmt.Errorf("runner: exchange document stage must not be empty")
	}
	return exchangeDocumentFromEnvelope(meta, sections), nil
}

func parseExchangeEnvelope(raw string) (exchangeFrontMatter, map[string]string, error) {
	var meta exchangeFrontMatter
	body, err := frontmatter.Parse(strings.NewReader(raw), &meta)
	if err != nil {
		return exchangeFrontMatter{}, nil, fmt.Errorf("runner: parse exchange front matter: %w", err)
	}
	return meta, parseExchangeSections(string(body)), nil
}

func exchangeDocumentFromEnvelope(meta exchangeFrontMatter, sections map[string]string) *ExchangeDocument {
	return &ExchangeDocument{
		Kind:            meta.Kind,
		Version:         meta.Version,
		Stage:           meta.Stage,
		RunnerID:        meta.RunnerID,
		NodeID:          meta.NodeID,
		SkillName:       meta.SkillName,
		TaskDescription: meta.TaskDescription,
		CreatedAt:       meta.CreatedAt,
		Handoffs:        append([]ExchangeHandoff(nil), meta.Handoffs...),
		Resources:       append([]ExchangeResource(nil), meta.Resources...),
		Memo:            sections["memo"],
		Reasoning:       sections["reasoning"],
		Handoff:         sections["handoff"],
	}
}

// NormalizeExchangeMarkdown returns raw as a valid exchange markdown document.
//
// If raw is already a valid exchange document, missing default metadata is
// filled and the document is rendered in canonical form. Otherwise raw is
// treated as handoff text and wrapped in defaults.
func NormalizeExchangeMarkdown(raw string, defaults ExchangeDocument) (string, error) {
	if parsed, err := ParseExchangeMarkdown(raw); err == nil {
		return withExchangeDefaults(*parsed, defaults).Markdown()
	}
	if draft, err := ParseExchangeDraftMarkdown(raw); err == nil {
		return withExchangeDefaults(*draft, defaults).Markdown()
	}
	doc := withExchangeDefaults(ExchangeDocument{Handoff: strings.TrimSpace(raw)}, defaults)
	return doc.Markdown()
}

// IsExchangeMarkdown reports whether raw is a valid exchange markdown
// document.
func IsExchangeMarkdown(raw string) bool {
	_, err := ParseExchangeMarkdown(raw)
	return err == nil
}

// ParseExchangeDraftMarkdown parses model-produced exchange-like markdown that
// follows the document shape but has incomplete or non-canonical front matter.
// It preserves Memo, Reasoning, Handoff, handoffs, and resources so callers can
// normalize drafts without wrapping the entire markdown as opaque text.
func ParseExchangeDraftMarkdown(raw string) (*ExchangeDocument, error) {
	meta, sections, err := parseExchangeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if !exchangeKindLooksLikeDraft(meta.Kind) {
		return nil, fmt.Errorf("runner: exchange draft kind = %q", meta.Kind)
	}
	if !hasExchangeDraftSections(sections) {
		return nil, fmt.Errorf("runner: exchange draft missing Memo, Reasoning, or Handoff sections")
	}
	doc := exchangeDocumentFromEnvelope(meta, sections)
	if doc.Kind != ExchangeDocumentKind {
		doc.Kind = ""
	}
	if doc.Version != ExchangeDocumentVersion {
		doc.Version = 0
	}
	return doc, nil
}

// LooksLikeExchangeMarkdown reports whether raw is either a canonical exchange
// document or a model-produced draft that can be normalized into one.
func LooksLikeExchangeMarkdown(raw string) bool {
	if IsExchangeMarkdown(raw) {
		return true
	}
	_, err := ParseExchangeDraftMarkdown(raw)
	return err == nil
}

type exchangeFrontMatter struct {
	Kind            string             `yaml:"kind"`
	Version         int                `yaml:"version"`
	Stage           ExchangeStage      `yaml:"stage"`
	RunnerID        string             `yaml:"runner_id,omitempty"`
	NodeID          string             `yaml:"node_id,omitempty"`
	SkillName       string             `yaml:"skill_name,omitempty"`
	TaskDescription string             `yaml:"task_description,omitempty"`
	CreatedAt       time.Time          `yaml:"created_at,omitempty"`
	Handoffs        []ExchangeHandoff  `yaml:"handoffs,omitempty"`
	Resources       []ExchangeResource `yaml:"resources,omitempty"`
}

func withExchangeDefaults(doc ExchangeDocument, defaults ExchangeDocument) ExchangeDocument {
	if doc.Kind == "" {
		if defaults.Kind != "" {
			doc.Kind = defaults.Kind
		} else {
			doc.Kind = ExchangeDocumentKind
		}
	}
	if doc.Version == 0 {
		if defaults.Version != 0 {
			doc.Version = defaults.Version
		} else {
			doc.Version = ExchangeDocumentVersion
		}
	}
	if doc.Stage == "" {
		doc.Stage = defaults.Stage
	}
	if doc.RunnerID == "" {
		doc.RunnerID = defaults.RunnerID
	}
	if doc.NodeID == "" {
		doc.NodeID = defaults.NodeID
	}
	if doc.SkillName == "" {
		doc.SkillName = defaults.SkillName
	}
	if doc.TaskDescription == "" {
		doc.TaskDescription = defaults.TaskDescription
	}
	if doc.CreatedAt.IsZero() {
		if !defaults.CreatedAt.IsZero() {
			doc.CreatedAt = defaults.CreatedAt
		} else {
			doc.CreatedAt = time.Now()
		}
	}
	if len(doc.Handoffs) == 0 {
		doc.Handoffs = append([]ExchangeHandoff(nil), defaults.Handoffs...)
	}
	if len(doc.Resources) == 0 {
		doc.Resources = append([]ExchangeResource(nil), defaults.Resources...)
	}
	if doc.Memo == "" {
		doc.Memo = defaults.Memo
	}
	if doc.Reasoning == "" {
		doc.Reasoning = defaults.Reasoning
	}
	if doc.Handoff == "" {
		doc.Handoff = defaults.Handoff
	}
	return doc
}

func writeExchangeSection(b *strings.Builder, title string, content string) {
	b.WriteString("\n## ")
	b.WriteString(title)
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(content))
	b.WriteString("\n")
}

func parseExchangeSections(body string) map[string]string {
	sections := map[string]string{
		"memo":      "",
		"reasoning": "",
		"handoff":   "",
	}
	var current string
	var buf bytes.Buffer
	flush := func() {
		if current == "" {
			buf.Reset()
			return
		}
		sections[current] = strings.Trim(buf.String(), "\n")
		buf.Reset()
	}

	for _, line := range strings.Split(body, "\n") {
		switch exchangeSectionName(line) {
		case "memo", "reasoning", "handoff":
			flush()
			current = exchangeSectionName(line)
		default:
			if current != "" {
				buf.WriteString(line)
				buf.WriteByte('\n')
			}
		}
	}
	flush()
	return sections
}

func exchangeKindLooksLikeDraft(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", ExchangeDocumentKind, "exchange", "dango_exchange", "dango-exchange":
		return true
	default:
		return false
	}
}

func hasExchangeDraftSections(sections map[string]string) bool {
	return strings.TrimSpace(sections["memo"]) != "" ||
		strings.TrimSpace(sections["reasoning"]) != "" ||
		strings.TrimSpace(sections["handoff"]) != ""
}

func exchangeSectionName(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return ""
	}
	name := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
	switch strings.ToLower(name) {
	case "memo":
		return "memo"
	case "reasoning":
		return "reasoning"
	case "handoff":
		return "handoff"
	default:
		return ""
	}
}
