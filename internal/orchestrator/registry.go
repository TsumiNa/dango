package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/tsumina/dango/internal/layout"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/runtime"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
	"gopkg.in/yaml.v3"
)

// RegistryService manages tool registration and registry persistence.
type RegistryService struct {
	layout  *layout.Layout
	store   *sqlite.Store
	runtime runtime.ContainerRuntime
	logger  *slog.Logger
}

// NewRegistryService constructs the tool registration service used by the
// orchestrator.
func NewRegistryService(layout *layout.Layout, store *sqlite.Store, rt runtime.ContainerRuntime, logger *slog.Logger) *RegistryService {
	return &RegistryService{
		layout:  layout,
		store:   store,
		runtime: rt,
		logger:  logging.Component(logger, "orchestrator.registry"),
	}
}

// RegisteredTool reports the persisted result of a registration operation.
type RegisteredTool struct {
	// Tool is the merged tool specification visible to the orchestrator.
	Tool spec.ToolSpec `json:"tool"`
	// Image is the runtime image or reference registered for the tool.
	Image string `json:"image"`
	// Row is the SQLite row persisted for the registration.
	Row sqlite.ToolRecord `json:"row"`
	// Files records the materialized config file locations on disk.
	Files RegisteredToolFiles `json:"files"`
}

// RegisteredToolFiles points at the persisted registration files for a tool.
type RegisteredToolFiles struct {
	// ToolPath is the captured tool.yaml emitted by executor describe.
	ToolPath string `json:"tool_path"`
	// OverridePath is the copied override.yaml when an override was provided.
	OverridePath string `json:"override_path,omitempty"`
	// MergedPath is the merged.yaml path used at execution time.
	MergedPath string `json:"merged_path"`
}

// Register pulls or resolves image, reads its tool description, merges any
// override, and persists the registration.
func (s *RegistryService) Register(ctx context.Context, image, overridePath string) (*RegisteredTool, error) {
	if s.runtime == nil {
		return nil, fmt.Errorf("container runtime is not configured")
	}
	s.logger.Info("registering tool", "image", image, "override_path", overridePath)
	if err := s.layout.Ensure(); err != nil {
		s.logger.Error("failed to ensure layout", "image", image, "error", err)
		return nil, err
	}

	if err := s.runtime.Pull(ctx, image); err != nil {
		s.logger.Error("failed to pull tool image", "image", image, "error", err)
		return nil, err
	}

	toolYAML, err := s.runtime.DescribeTool(ctx, image)
	if err != nil {
		s.logger.Error("failed to describe tool image", "image", image, "error", err)
		return nil, err
	}

	var described spec.ToolSpec
	if err := yaml.Unmarshal(toolYAML, &described); err != nil {
		s.logger.Error("failed to parse tool description", "image", image, "error", err)
		return nil, fmt.Errorf("parse tool.yaml from image %q: %w", image, err)
	}
	if err := described.Validate(); err != nil {
		s.logger.Error("described tool failed validation", "image", image, "error", err)
		return nil, fmt.Errorf("invalid tool description in image %q: %w", image, err)
	}

	override, rawOverride, err := loadOverride(overridePath)
	if err != nil {
		s.logger.Error("failed to load override", "image", image, "override_path", overridePath, "error", err)
		return nil, err
	}

	merged, err := spec.MergeToolSpec(described, override)
	if err != nil {
		s.logger.Error("failed to merge tool spec", "image", image, "tool", described.Name, "error", err)
		return nil, err
	}

	if err := s.layout.EnsureToolDir(merged.Name); err != nil {
		s.logger.Error("failed to ensure tool directory", "tool", merged.Name, "error", err)
		return nil, err
	}

	toolPath := s.layout.ToolSpecPath(merged.Name)
	if err := os.WriteFile(toolPath, toolYAML, 0o644); err != nil {
		s.logger.Error("failed to write tool spec", "tool", merged.Name, "path", toolPath, "error", err)
		return nil, fmt.Errorf("write tool.yaml: %w", err)
	}

	overrideFilePath := ""
	if len(rawOverride) > 0 {
		overrideFilePath = s.layout.ToolOverridePath(merged.Name)
		if err := os.WriteFile(overrideFilePath, rawOverride, 0o644); err != nil {
			s.logger.Error("failed to write override file", "tool", merged.Name, "path", overrideFilePath, "error", err)
			return nil, fmt.Errorf("write override.yaml: %w", err)
		}
	}

	mergedPayload, err := yaml.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal merged.yaml: %w", err)
	}
	mergedPath := s.layout.ToolMergedPath(merged.Name)
	if err := os.WriteFile(mergedPath, mergedPayload, 0o644); err != nil {
		s.logger.Error("failed to write merged config", "tool", merged.Name, "path", mergedPath, "error", err)
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
		s.logger.Error("failed to persist tool registration", "tool", merged.Name, "error", err)
		return nil, err
	}

	persisted, err := s.store.GetTool(ctx, merged.Name)
	if err != nil {
		s.logger.Error("failed to reload persisted tool", "tool", merged.Name, "error", err)
		return nil, err
	}

	s.logger.Info("tool registered", "tool", merged.Name, "image", image, "inputs", merged.InputTypes, "outputs", merged.OutputTypes)

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

// Unregister removes a tool registration and its persisted config files.
func (s *RegistryService) Unregister(ctx context.Context, name string) error {
	s.logger.Info("unregistering tool", "tool", name)
	if err := s.store.DeleteTool(ctx, name); err != nil && !s.store.IsNotFound(err) {
		s.logger.Error("failed to delete tool record", "tool", name, "error", err)
		return err
	}

	toolDir := s.layout.ToolDir(name)
	if err := os.RemoveAll(toolDir); err != nil {
		s.logger.Error("failed to remove tool directory", "tool", name, "path", toolDir, "error", err)
		return fmt.Errorf("remove tool directory %q: %w", toolDir, err)
	}
	s.logger.Info("tool unregistered", "tool", name)
	return nil
}

// List returns the currently registered tools.
func (s *RegistryService) List(ctx context.Context) ([]sqlite.ToolRecord, error) {
	return s.store.ListTools(ctx)
}

// Load returns one registered tool row by name.
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
