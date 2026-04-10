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

	"github.com/tsumina/dango/internal/datadir"
	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/runner/runtime"
	"github.com/tsumina/dango/internal/spec"
	"github.com/tsumina/dango/internal/store/sqlite"
	"gopkg.in/yaml.v3"
)

// RegistryService manages the control-plane path from a tool reference to a
// persisted registry entry.
//
// A RegistryService coordinates the runtime, filesystem layout, and SQLite
// store needed to register or remove tools. The zero value is not usable;
// callers construct the service with [NewRegistryService] and then invoke
// [RegistryService.Register], [RegistryService.Unregister], [RegistryService.List],
// or [RegistryService.Load] from CLI or HTTP handlers.
type RegistryService struct {
	locator *datadir.Locator
	store   *sqlite.Store
	runtime runtime.ContainerRuntime
	logger  *slog.Logger
}

// NewRegistryService constructs the registry service used by the orchestrator
// control plane.
//
// The returned service expects locator and store to point at the same data
// root. rt is used to pull and describe tools during registration, while logger
// is wrapped with the orchestrator.registry component name.
func NewRegistryService(locator *datadir.Locator, store *sqlite.Store, rt runtime.ContainerRuntime, logger *slog.Logger) *RegistryService {
	return &RegistryService{
		locator: locator,
		store:   store,
		runtime: rt,
		logger:  logging.Component(logger, "orchestrator.registry"),
	}
}

// RegisteredTool reports the full persisted result of one registration
// operation.
//
// It combines the merged tool spec that planners should see, the SQLite row
// that was stored, and the materialized file paths written under the registry
// directory.
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

// RegisteredToolFiles points at the registry files written for a tool during
// registration.
//
// These paths let callers inspect the raw described tool spec, the optional
// override payload, and the final merged configuration that the runner passes
// into executor planning and execution.
type RegisteredToolFiles struct {
	// ToolPath is the captured tool.yaml emitted by executor describe.
	ToolPath string `json:"tool_path"`
	// OverridePath is the copied override.yaml when an override was provided.
	OverridePath string `json:"override_path,omitempty"`
	// MergedPath is the merged.yaml path used at execution time.
	MergedPath string `json:"merged_path"`
}

// Register resolves image, reads its described tool spec, merges any override,
// writes the registry files, and persists the final tool row.
//
// The registration workflow is: ensure the data root exists, ask the runtime
// to make the tool available, read tool.yaml through the runtime, validate the
// described [spec.ToolSpec], merge any override.yaml content, write tool.yaml,
// override.yaml, and merged.yaml under the registry directory, and finally
// upsert the SQLite row. Register updates existing rows and files when the tool
// name already exists.
func (s *RegistryService) Register(ctx context.Context, image, overridePath string) (*RegisteredTool, error) {
	if s.runtime == nil {
		return nil, fmt.Errorf("container runtime is not configured")
	}
	s.logger.Info("registering tool", "image", image, "override_path", overridePath)
	if err := s.locator.Ensure(); err != nil {
		s.logger.Error("failed to ensure data dir", "image", image, "error", err)
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

	if err := s.locator.EnsureToolDir(merged.Name); err != nil {
		s.logger.Error("failed to ensure tool directory", "tool", merged.Name, "error", err)
		return nil, err
	}

	toolPath := s.locator.ToolSpecPath(merged.Name)
	if err := os.WriteFile(toolPath, toolYAML, 0o644); err != nil {
		s.logger.Error("failed to write tool spec", "tool", merged.Name, "path", toolPath, "error", err)
		return nil, fmt.Errorf("write tool.yaml: %w", err)
	}

	overrideFilePath := ""
	if len(rawOverride) > 0 {
		overrideFilePath = s.locator.ToolOverridePath(merged.Name)
		if err := os.WriteFile(overrideFilePath, rawOverride, 0o644); err != nil {
			s.logger.Error("failed to write override file", "tool", merged.Name, "path", overrideFilePath, "error", err)
			return nil, fmt.Errorf("write override.yaml: %w", err)
		}
	}

	mergedPayload, err := yaml.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal merged.yaml: %w", err)
	}
	mergedPath := s.locator.ToolMergedPath(merged.Name)
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

// Unregister removes a registered tool row and deletes its registry files.
//
// Unregister is idempotent for missing database rows and still attempts to
// remove the tool directory so filesystem state converges with the database.
// It does not ask the runtime to delete or prune any underlying image.
func (s *RegistryService) Unregister(ctx context.Context, name string) error {
	s.logger.Info("unregistering tool", "tool", name)
	if err := s.store.DeleteTool(ctx, name); err != nil && !s.store.IsNotFound(err) {
		s.logger.Error("failed to delete tool record", "tool", name, "error", err)
		return err
	}

	toolDir := s.locator.ToolDir(name)
	if err := os.RemoveAll(toolDir); err != nil {
		s.logger.Error("failed to remove tool directory", "tool", name, "path", toolDir, "error", err)
		return fmt.Errorf("remove tool directory %q: %w", toolDir, err)
	}
	s.logger.Info("tool unregistered", "tool", name)
	return nil
}

// List returns the current registry rows ordered by the store implementation.
//
// List is row-oriented and does not read back the registry files on disk.
func (s *RegistryService) List(ctx context.Context) ([]sqlite.ToolRecord, error) {
	return s.store.ListTools(ctx)
}

// Load returns the persisted registry row for one tool name.
//
// Load is the row-oriented lookup used by higher-level orchestrator and runner
// code. It returns sql.ErrNoRows when the tool is not registered.
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
