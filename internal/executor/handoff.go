package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tsumina/dango/internal/spec"
)

func writeFailureHandoffs(publicOutputPath, privateOutputPath, toolName, taskID string, executionErr error) error {
	return writeFailureHandoffsWithSummary(publicOutputPath, privateOutputPath, toolName, taskID, "Tool execution failed before producing a valid handoff.", executionErr)
}

func writeFailureHandoffsWithSummary(publicOutputPath, privateOutputPath, toolName, taskID string, summary string, executionErr error) error {
	handoff := spec.Handoff{
		Metadata: spec.HandoffMetadata{
			TaskID:    taskID,
			Tool:      toolName,
			Status:    spec.HandoffStatusFailed,
			Timestamp: time.Now().UTC(),
			Error:     executionErr.Error(),
		},
		Body: "## Description\n\n" + strings.TrimSpace(summary),
	}
	if err := writePublicHandoff(publicOutputPath, handoff); err != nil {
		return err
	}
	return writePrivateHandoff(privateOutputPath, handoff)
}

func writePublicHandoff(outputPath string, handoff spec.Handoff) error {
	return writeHandoffFile(filepath.Join(outputPath, "handoff.md"), handoff)
}

func writePrivateHandoff(outputPath string, handoff spec.Handoff) error {
	return writeHandoffFile(filepath.Join(outputPath, "_handoff.md"), handoff)
}

func ensurePublicHandoff(publicOutputPath, privateOutputPath string) error {
	publicPath := filepath.Join(publicOutputPath, "handoff.md")
	if _, err := os.Stat(publicPath); err == nil {
		return nil
	}

	privatePayload, err := os.ReadFile(filepath.Join(privateOutputPath, "_handoff.md"))
	if err != nil {
		return fmt.Errorf("read private handoff %q: %w", filepath.Join(privateOutputPath, "_handoff.md"), err)
	}
	if err := os.WriteFile(publicPath, privatePayload, 0o644); err != nil {
		return fmt.Errorf("write public handoff %q: %w", publicPath, err)
	}
	return nil
}

func writeHandoffFile(path string, handoff spec.Handoff) error {
	payload, err := spec.RenderHandoff(handoff)
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("write handoff %q: %w", path, err)
	}
	return nil
}
