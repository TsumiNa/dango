package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const HostPrefix = "host://"

type HostRuntime struct{}

func NewHostRuntime() *HostRuntime {
	return &HostRuntime{}
}

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

func (h *HostRuntime) Pull(_ context.Context, image string) error {
	_, err := resolveHostToolDir(image)
	return err
}

func (h *HostRuntime) DescribeTool(_ context.Context, image string) ([]byte, error) {
	dir, err := resolveHostToolDir(image)
	if err != nil {
		return nil, err
	}

	toolPath := filepath.Join(dir, "tool.yaml")
	payload, err := os.ReadFile(toolPath)
	if err != nil {
		return nil, fmt.Errorf("read host tool spec %q: %w", toolPath, err)
	}
	return payload, nil
}

func (h *HostRuntime) RunExecutor(ctx context.Context, request ExecutorRunRequest) error {
	dir, err := resolveHostToolDir(request.Image)
	if err != nil {
		return err
	}

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
	if request.OutputHost != "" {
		env = append(env, "OUTPUT_PATH="+request.OutputHost)
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
		return fmt.Errorf("run host tool %q: %w: %s", dir, err, bytes.TrimSpace(output))
	}

	return nil
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
