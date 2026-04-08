package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/orchestrator"
	"github.com/tsumina/dango/internal/runtime"
)

func ensureDemoToolsRegistered(ctx context.Context, registry *orchestrator.RegistryService, toolsDir string, logger *slog.Logger) error {
	absoluteToolsDir, err := filepath.Abs(toolsDir)
	if err != nil {
		return fmt.Errorf("resolve demo tools dir %q: %w", toolsDir, err)
	}
	logger = logging.Component(logger, "cli.demo-tools")
	logger.Info("registering demo tools", "tools_dir", absoluteToolsDir)

	entries, err := os.ReadDir(absoluteToolsDir)
	if err != nil {
		return fmt.Errorf("read demo tools dir %q: %w", absoluteToolsDir, err)
	}

	var refs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		toolDir := filepath.Join(absoluteToolsDir, entry.Name())
		if _, err := os.Stat(filepath.Join(toolDir, "tool.yaml")); err != nil {
			continue
		}
		refs = append(refs, runtime.HostPrefix+toolDir)
	}

	sort.Strings(refs)
	if len(refs) == 0 {
		return fmt.Errorf("no demo tools found in %q", absoluteToolsDir)
	}

	for _, ref := range refs {
		logger.Debug("registering demo tool", "ref", ref)
		if _, err := registry.Register(ctx, ref, ""); err != nil {
			return fmt.Errorf("register demo tool %q: %w", ref, err)
		}
	}

	return nil
}
