package spec

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	HandoffStatusCompleted = "completed"
	HandoffStatusFailed    = "failed"
	HandoffStatusPartial   = "partial"
)

type Handoff struct {
	Metadata HandoffMetadata
	Body     string
}

type HandoffMetadata struct {
	TaskID      string    `json:"task_id" yaml:"task_id"`
	Tool        string    `json:"tool" yaml:"tool"`
	Status      string    `json:"status" yaml:"status"`
	OutputFiles []string  `json:"output_files,omitempty" yaml:"output_files,omitempty"`
	NextTool    *string   `json:"next_tool,omitempty" yaml:"next_tool,omitempty"`
	Timestamp   time.Time `json:"timestamp" yaml:"timestamp"`
	Error       string    `json:"error,omitempty" yaml:"error,omitempty"`
}

func (m HandoffMetadata) Validate() error {
	if strings.TrimSpace(m.TaskID) == "" {
		return fmt.Errorf("task_id is required")
	}
	if strings.TrimSpace(m.Tool) == "" {
		return fmt.Errorf("tool is required")
	}
	switch m.Status {
	case HandoffStatusCompleted, HandoffStatusFailed, HandoffStatusPartial:
	default:
		return fmt.Errorf("unsupported handoff status %q", m.Status)
	}
	if m.Timestamp.IsZero() {
		return fmt.Errorf("timestamp is required")
	}
	if m.Status != HandoffStatusCompleted && strings.TrimSpace(m.Error) == "" {
		return fmt.Errorf("error is required when status is not completed")
	}

	return nil
}

func ParseHandoff(data []byte) (Handoff, error) {
	lines, end, err := splitFrontmatter(data)
	if err != nil {
		return Handoff{}, err
	}
	var metadata HandoffMetadata
	frontmatter := strings.Join(lines[1:end], "\n")
	if err := yaml.Unmarshal([]byte(frontmatter), &metadata); err != nil {
		return Handoff{}, fmt.Errorf("parse handoff frontmatter: %w", err)
	}
	if err := metadata.Validate(); err != nil {
		return Handoff{}, err
	}

	body := ""
	if end+1 < len(lines) {
		body = strings.Join(lines[end+1:], "\n")
	}

	return Handoff{
		Metadata: metadata,
		Body:     strings.TrimLeft(body, "\n"),
	}, nil
}

func ExtractHandoffFrontmatter(data []byte) ([]byte, error) {
	lines, end, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}

	return []byte(strings.Join(lines[1:end], "\n")), nil
}

func RenderHandoff(h Handoff) ([]byte, error) {
	if err := h.Metadata.Validate(); err != nil {
		return nil, err
	}

	frontmatter, err := yaml.Marshal(h.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal handoff frontmatter: %w", err)
	}

	body := strings.TrimSpace(h.Body)
	if body == "" {
		return []byte(fmt.Sprintf("---\n%s---\n", string(frontmatter))), nil
	}

	return []byte(fmt.Sprintf("---\n%s---\n\n%s\n", string(frontmatter), body)), nil
}

func splitFrontmatter(data []byte) ([]string, int, error) {
	content := string(data)
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return nil, -1, fmt.Errorf("handoff frontmatter must start with ---")
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return nil, -1, fmt.Errorf("handoff frontmatter closing --- not found")
	}

	return lines, end, nil
}
