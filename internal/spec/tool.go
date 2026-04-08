package spec

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ToolSpec describes a registered execution tool and its default behavior.
type ToolSpec struct {
	// Name uniquely identifies the tool in the registry and DAG plans.
	Name string `json:"name" yaml:"name"`
	// Version reports the tool's own version string.
	Version string `json:"version" yaml:"version"`
	// Description summarizes the tool's capabilities for planning and review.
	Description string `json:"description" yaml:"description"`
	// InputTypes lists the logical input formats the tool can accept.
	InputTypes []string `json:"input_types" yaml:"input_types"`
	// OutputTypes lists the logical output formats the tool can produce.
	OutputTypes []string `json:"output_types" yaml:"output_types"`
	// Model identifies the model configured for the tool in the current spec.
	Model string `json:"model" yaml:"model"`
	// Defaults stores tool-specific default configuration values.
	Defaults map[string]any `json:"defaults,omitempty" yaml:"defaults,omitempty"`
}

// Validate checks whether the tool spec contains the minimum required fields.
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

// ToMap converts the tool spec to a generic map representation suitable for
// layered merges.
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

func toolSpecFromMap(value map[string]any) (ToolSpec, error) {
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
