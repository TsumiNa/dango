package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tsumina/dango/internal/spec"
	"gopkg.in/yaml.v3"
)

func loadToolSpec() (spec.ToolSpec, error) {
	for _, candidate := range []string{
		strings.TrimSpace(os.Getenv("DANGO_TOOL_YAML")),
		"/opt/tool/tool.yaml",
		"tool.yaml",
	} {
		if candidate == "" {
			continue
		}
		toolSpec, err := loadToolSpecFrom(candidate)
		if err == nil {
			return toolSpec, nil
		}
	}

	return spec.ToolSpec{}, fmt.Errorf("tool.yaml was not found via DANGO_TOOL_YAML, /opt/tool/tool.yaml, or ./tool.yaml")
}

func loadToolSpecFrom(path string) (spec.ToolSpec, error) {
	if strings.TrimSpace(path) == "" {
		return spec.ToolSpec{}, fmt.Errorf("tool spec path is empty")
	}

	payload, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return spec.ToolSpec{}, fmt.Errorf("read tool spec %q: %w", path, err)
	}

	var toolSpec spec.ToolSpec
	if err := yaml.Unmarshal(payload, &toolSpec); err != nil {
		return spec.ToolSpec{}, fmt.Errorf("parse tool spec %q: %w", path, err)
	}

	if err := toolSpec.Validate(); err != nil {
		return spec.ToolSpec{}, err
	}

	return toolSpec, nil
}

func resolveRunHook() string {
	for _, candidate := range []string{
		strings.TrimSpace(os.Getenv("DANGO_TOOL_RUN")),
		"/opt/tool/run",
		"/opt/tool/bin/run",
	} {
		if candidate == "" {
			continue
		}

		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}

		if info.Mode()&0o111 != 0 {
			return candidate
		}
	}

	return ""
}
