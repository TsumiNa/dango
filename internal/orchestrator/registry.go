package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tsumina/dango/internal/layout"
	"github.com/tsumina/dango/internal/runtime"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
	"gopkg.in/yaml.v3"
)

type RegistryService struct {
	layout  *layout.Layout
	store   *sqlite.Store
	runtime runtime.ContainerRuntime
}

func NewRegistryService(layout *layout.Layout, store *sqlite.Store, rt runtime.ContainerRuntime) *RegistryService {
	return &RegistryService{
		layout:  layout,
		store:   store,
		runtime: rt,
	}
}

type RegisteredTool struct {
	Tool  spec.ToolSpec       `json:"tool"`
	Image string              `json:"image"`
	Row   sqlite.ToolRecord   `json:"row"`
	Files RegisteredToolFiles `json:"files"`
}

type RegisteredToolFiles struct {
	ToolPath     string `json:"tool_path"`
	OverridePath string `json:"override_path,omitempty"`
	MergedPath   string `json:"merged_path"`
}

func (s *RegistryService) Register(ctx context.Context, image, overridePath string) (*RegisteredTool, error) {
	if s.runtime == nil {
		return nil, fmt.Errorf("container runtime is not configured")
	}
	if err := s.layout.Ensure(); err != nil {
		return nil, err
	}

	if err := s.runtime.Pull(ctx, image); err != nil {
		return nil, err
	}

	toolYAML, err := s.runtime.DescribeTool(ctx, image)
	if err != nil {
		return nil, err
	}

	var described spec.ToolSpec
	if err := yaml.Unmarshal(toolYAML, &described); err != nil {
		return nil, fmt.Errorf("parse tool.yaml from image %q: %w", image, err)
	}
	if err := described.Validate(); err != nil {
		return nil, fmt.Errorf("invalid tool description in image %q: %w", image, err)
	}

	override, rawOverride, err := loadOverride(overridePath)
	if err != nil {
		return nil, err
	}

	merged, err := spec.MergeToolSpec(described, override)
	if err != nil {
		return nil, err
	}

	if err := s.layout.EnsureToolDir(merged.Name); err != nil {
		return nil, err
	}

	toolPath := s.layout.ToolSpecPath(merged.Name)
	if err := os.WriteFile(toolPath, toolYAML, 0o644); err != nil {
		return nil, fmt.Errorf("write tool.yaml: %w", err)
	}

	overrideFilePath := ""
	if len(rawOverride) > 0 {
		overrideFilePath = s.layout.ToolOverridePath(merged.Name)
		if err := os.WriteFile(overrideFilePath, rawOverride, 0o644); err != nil {
			return nil, fmt.Errorf("write override.yaml: %w", err)
		}
	}

	mergedPayload, err := yaml.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal merged.yaml: %w", err)
	}
	mergedPath := s.layout.ToolMergedPath(merged.Name)
	if err := os.WriteFile(mergedPath, mergedPayload, 0o644); err != nil {
		return nil, fmt.Errorf("write merged.yaml: %w", err)
	}

	configJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal tool config json: %w", err)
	}

	row := sqlite.ToolRecord{
		Name:       merged.Name,
		Image:      image,
		ConfigJSON: string(configJSON),
	}
	if err := s.store.UpsertTool(ctx, row); err != nil {
		return nil, err
	}

	persisted, err := s.store.GetTool(ctx, merged.Name)
	if err != nil {
		return nil, err
	}

	return &RegisteredTool{
		Tool:  merged,
		Image: image,
		Row:   persisted,
		Files: RegisteredToolFiles{
			ToolPath:     toolPath,
			OverridePath: overrideFilePath,
			MergedPath:   mergedPath,
		},
	}, nil
}

func (s *RegistryService) Unregister(ctx context.Context, name string) error {
	if err := s.store.DeleteTool(ctx, name); err != nil && !s.store.IsNotFound(err) {
		return err
	}

	toolDir := s.layout.ToolDir(name)
	if err := os.RemoveAll(toolDir); err != nil {
		return fmt.Errorf("remove tool directory %q: %w", toolDir, err)
	}
	return nil
}

func (s *RegistryService) List(ctx context.Context) ([]sqlite.ToolRecord, error) {
	return s.store.ListTools(ctx)
}

func (s *RegistryService) Load(ctx context.Context, name string) (sqlite.ToolRecord, error) {
	record, err := s.store.GetTool(ctx, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sqlite.ToolRecord{}, sql.ErrNoRows
		}
		return sqlite.ToolRecord{}, err
	}
	return record, nil
}

func loadOverride(path string) (map[string]any, []byte, error) {
	if strings.TrimSpace(path) == "" {
		return map[string]any{}, nil, nil
	}

	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, nil, fmt.Errorf("read override file %q: %w", path, err)
	}

	var override map[string]any
	if err := yaml.Unmarshal(raw, &override); err != nil {
		return nil, nil, fmt.Errorf("parse override file %q: %w", path, err)
	}

	return override, raw, nil
}
