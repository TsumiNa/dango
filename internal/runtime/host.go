package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/spec"
	"gopkg.in/yaml.v3"
)

// HostPrefix identifies tool references that should be resolved against the
// host-local demo runtime instead of the container runtime.
const HostPrefix = "host://"

type hostRuntime struct {
	logger *slog.Logger
}

func newHostRuntime(logger *slog.Logger) *hostRuntime {
	return &hostRuntime{
		logger: logging.Component(logger, "runtime.host"),
	}
}

// NormalizeImageReference resolves host-based image references into absolute
// host paths while leaving container image references unchanged.
func NormalizeImageReference(image string) (string, error) {
	if !strings.HasPrefix(image, HostPrefix) {
		return strings.TrimSpace(image), nil
	}

	dir, err := resolveHostToolDir(image)
	if err != nil {
		return "", err
	}

	return HostPrefix + dir, nil
}

func (h *hostRuntime) Pull(_ context.Context, image string) error {
	_, err := resolveHostToolDir(image)
	return err
}

func (h *hostRuntime) DescribeTool(_ context.Context, image string) ([]byte, error) {
	dir, err := resolveHostToolDir(image)
	if err != nil {
		return nil, err
	}
	h.logger.Debug("describing host tool", "image", image, "dir", dir)

	toolPath := filepath.Join(dir, "tool.yaml")
	payload, err := os.ReadFile(toolPath)
	if err != nil {
		return nil, fmt.Errorf("read host tool spec %q: %w", toolPath, err)
	}
	return payload, nil
}

func (h *hostRuntime) PlanExecutor(ctx context.Context, request ExecutorPlanRequest) ([]byte, error) {
	dir, err := resolveHostToolDir(request.Image)
	if err != nil {
		return nil, err
	}
	h.logger.Info("planning host tool", "image", request.Image, "dir", dir, "task_id", request.TaskID)

	planPath := filepath.Join(dir, "plan")
	if info, err := os.Stat(planPath); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		env := append([]string{}, os.Environ()...)
		env = append(env,
			"TASK_ID="+request.TaskID,
			"SUB_TASK="+request.SubTaskHost,
			"TOOL_CONFIG="+request.ToolConfigHost,
			"DANGO_TOOL_YAML="+filepath.Join(dir, "tool.yaml"),
			"DANGO_TOOL_PLAN="+planPath,
		)

		cmd := exec.CommandContext(ctx, planPath)
		cmd.Dir = dir
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		if err != nil {
			h.logger.Error("host tool planning hook failed", "dir", dir, "task_id", request.TaskID, "error", err, "output", string(bytes.TrimSpace(output)))
			return nil, fmt.Errorf("run host tool planner %q: %w: %s", dir, err, bytes.TrimSpace(output))
		}
		return bytes.TrimSpace(output), nil
	}

	toolPayload, err := os.ReadFile(filepath.Join(dir, "tool.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read host tool spec %q: %w", filepath.Join(dir, "tool.yaml"), err)
	}
	var toolSpec spec.ToolSpec
	if err := yaml.Unmarshal(toolPayload, &toolSpec); err != nil {
		return nil, fmt.Errorf("parse host tool spec %q: %w", filepath.Join(dir, "tool.yaml"), err)
	}
	if err := toolSpec.Validate(); err != nil {
		return nil, err
	}

	rawSubTask, err := os.ReadFile(request.SubTaskHost)
	if err != nil {
		return nil, fmt.Errorf("read planning sub-task %q: %w", request.SubTaskHost, err)
	}

	plan := spec.ExecutorPlan{
		Summary:         fmt.Sprintf("Use %s to complete the planned stage for this task.", toolSpec.Name),
		SubTask:         strings.TrimSpace(string(rawSubTask)),
		ExpectedOutputs: defaultExpectedOutputs(toolSpec),
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("marshal host planning result: %w", err)
	}
	return payload, nil
}

func (h *hostRuntime) RunExecutor(ctx context.Context, request ExecutorRunRequest) error {
	dir, err := resolveHostToolDir(request.Image)
	if err != nil {
		return err
	}
	h.logger.Info("running host tool", "image", request.Image, "dir", dir, "task_id", request.TaskID)

	runPath := filepath.Join(dir, "run")
	info, err := os.Stat(runPath)
	if err != nil {
		return fmt.Errorf("host tool run hook %q: %w", runPath, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("host tool run hook %q must be an executable file", runPath)
	}

	toolPath := filepath.Join(dir, "tool.yaml")
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"TASK_ID="+request.TaskID,
		"SUB_TASK="+request.SubTaskHost,
		"TOOL_CONFIG="+request.ToolConfigHost,
		"DANGO_TOOL_YAML="+toolPath,
		"DANGO_TOOL_RUN="+runPath,
	)
	if request.InputHost != "" {
		env = append(env, "INPUT_PATH="+request.InputHost)
	}
	if request.PublicOutputHost != "" {
		env = append(env,
			"OUTPUT_PATH="+request.PublicOutputHost,
			"PUBLIC_OUTPUT_PATH="+request.PublicOutputHost,
		)
	}
	if request.PrivateOutputHost != "" {
		env = append(env, "PRIVATE_OUTPUT_PATH="+request.PrivateOutputHost)
	}
	if request.InputURL != "" {
		env = append(env, "INPUT_URL="+request.InputURL)
	}
	if request.OutputURL != "" {
		env = append(env, "OUTPUT_URL="+request.OutputURL)
	}

	cmd := exec.CommandContext(ctx, runPath)
	cmd.Dir = dir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		h.logger.Error("host tool failed", "dir", dir, "task_id", request.TaskID, "error", err, "output", string(bytes.TrimSpace(output)))
		return fmt.Errorf("run host tool %q: %w: %s", dir, err, bytes.TrimSpace(output))
	}

	h.logger.Info("host tool completed", "dir", dir, "task_id", request.TaskID)
	return nil
}

func defaultExpectedOutputs(toolSpec spec.ToolSpec) []string {
	if len(toolSpec.OutputTypes) == 0 {
		return nil
	}

	outputs := make([]string, 0, len(toolSpec.OutputTypes))
	for _, outputType := range toolSpec.OutputTypes {
		outputType = strings.TrimSpace(outputType)
		if outputType == "" {
			continue
		}
		outputs = append(outputs, "result."+outputType)
	}
	return outputs
}

func resolveHostToolDir(image string) (string, error) {
	if !strings.HasPrefix(image, HostPrefix) {
		return "", fmt.Errorf("host runtime requires image reference with %q prefix", HostPrefix)
	}

	rawPath := strings.TrimSpace(strings.TrimPrefix(image, HostPrefix))
	if rawPath == "" {
		return "", fmt.Errorf("host runtime image reference %q has empty path", image)
	}

	absolutePath, err := filepath.Abs(rawPath)
	if err != nil {
		return "", fmt.Errorf("resolve host tool path %q: %w", rawPath, err)
	}

	info, err := os.Stat(absolutePath)
	if err != nil {
		return "", fmt.Errorf("stat host tool directory %q: %w", absolutePath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("host tool path %q must be a directory", absolutePath)
	}

	return absolutePath, nil
}
