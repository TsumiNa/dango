package spec

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type ToolSpec struct {
	Name        string         `json:"name" yaml:"name"`
	Version     string         `json:"version" yaml:"version"`
	Description string         `json:"description" yaml:"description"`
	InputTypes  []string       `json:"input_types" yaml:"input_types"`
	OutputTypes []string       `json:"output_types" yaml:"output_types"`
	Model       string         `json:"model" yaml:"model"`
	Defaults    map[string]any `json:"defaults,omitempty" yaml:"defaults,omitempty"`
}

func (s ToolSpec) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("tool name is required")
	}
	if strings.TrimSpace(s.Version) == "" {
		return fmt.Errorf("tool version is required")
	}
	if strings.TrimSpace(s.Description) == "" {
		return fmt.Errorf("tool description is required")
	}
	if len(s.OutputTypes) == 0 {
		return fmt.Errorf("at least one output type is required")
	}

	return nil
}

func (s ToolSpec) ToMap() (map[string]any, error) {
	payload, err := yaml.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshal tool spec: %w", err)
	}

	var out map[string]any
	if err := yaml.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("unmarshal tool spec map: %w", err)
	}

	return out, nil
}

func ToolSpecFromMap(value map[string]any) (ToolSpec, error) {
	payload, err := yaml.Marshal(value)
	if err != nil {
		return ToolSpec{}, fmt.Errorf("marshal merged map: %w", err)
	}

	var out ToolSpec
	if err := yaml.Unmarshal(payload, &out); err != nil {
		return ToolSpec{}, fmt.Errorf("unmarshal merged tool spec: %w", err)
	}

	return out, nil
}
