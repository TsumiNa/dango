package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path"
	"strings"
)

type ContainerRuntime interface {
	Pull(ctx context.Context, image string) error
	DescribeTool(ctx context.Context, image string) ([]byte, error)
	RunExecutor(ctx context.Context, request ExecutorRunRequest) error
}

type ExecutorRunRequest struct {
	Image          string
	TaskID         string
	SubTaskHost    string
	ToolConfigHost string
	InputHost      string
	OutputHost     string
	InputURL       string
	OutputURL      string
}

func (r ExecutorRunRequest) InputContainerPath() string {
	return path.Join("/mnt/shared", r.TaskID, "input")
}

func (r ExecutorRunRequest) OutputContainerPath() string {
	return path.Join("/mnt/shared", r.TaskID, "output")
}

type MultiRuntime struct {
	docker ContainerRuntime
	host   ContainerRuntime
}

func NewDefault(dockerBinary string) *MultiRuntime {
	return &MultiRuntime{
		docker: NewDockerCLI(dockerBinary),
		host:   NewHostRuntime(),
	}
}

func (m *MultiRuntime) Pull(ctx context.Context, image string) error {
	rt, err := m.resolve(image)
	if err != nil {
		return err
	}
	return rt.Pull(ctx, image)
}

func (m *MultiRuntime) DescribeTool(ctx context.Context, image string) ([]byte, error) {
	rt, err := m.resolve(image)
	if err != nil {
		return nil, err
	}
	return rt.DescribeTool(ctx, image)
}

func (m *MultiRuntime) RunExecutor(ctx context.Context, request ExecutorRunRequest) error {
	rt, err := m.resolve(request.Image)
	if err != nil {
		return err
	}
	return rt.RunExecutor(ctx, request)
}

func (m *MultiRuntime) resolve(image string) (ContainerRuntime, error) {
	if strings.HasPrefix(image, HostPrefix) {
		if m.host == nil {
			return nil, fmt.Errorf("host runtime is not configured")
		}
		return m.host, nil
	}

	if m.docker == nil {
		return nil, fmt.Errorf("docker runtime is not configured")
	}
	return m.docker, nil
}

type DockerCLI struct {
	Binary string
}

func NewDockerCLI(binary string) *DockerCLI {
	if binary == "" {
		binary = "docker"
	}
	return &DockerCLI{Binary: binary}
}

func (d *DockerCLI) Pull(ctx context.Context, image string) error {
	cmd := exec.CommandContext(ctx, d.Binary, "pull", image)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pull image %q: %w: %s", image, err, bytes.TrimSpace(output))
	}
	return nil
}

func (d *DockerCLI) DescribeTool(ctx context.Context, image string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, d.Binary, "run", "--rm", image, "dango", "executor", "describe", "--format", "yaml")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("describe tool in image %q: %w: %s", image, err, bytes.TrimSpace(output))
	}
	return output, nil
}

func (d *DockerCLI) RunExecutor(ctx context.Context, request ExecutorRunRequest) error {
	args := []string{
		"run", "--rm",
		"-e", "TASK_ID=" + request.TaskID,
		"-e", "SUB_TASK=/etc/dango/sub-task.md",
		"-e", "TOOL_CONFIG=/etc/dango/tool-config.yaml",
	}

	if request.InputHost != "" {
		args = append(args,
			"-e", "INPUT_PATH="+request.InputContainerPath(),
			"-v", request.InputHost+":"+request.InputContainerPath()+":ro",
		)
	}
	if request.OutputHost != "" {
		args = append(args,
			"-e", "OUTPUT_PATH="+request.OutputContainerPath(),
			"-v", request.OutputHost+":"+request.OutputContainerPath()+":rw",
		)
	}
	if request.InputURL != "" {
		args = append(args, "-e", "INPUT_URL="+request.InputURL)
	}
	if request.OutputURL != "" {
		args = append(args, "-e", "OUTPUT_URL="+request.OutputURL)
	}
	if request.SubTaskHost != "" {
		args = append(args, "-v", request.SubTaskHost+":/etc/dango/sub-task.md:ro")
	}
	if request.ToolConfigHost != "" {
		args = append(args, "-v", request.ToolConfigHost+":/etc/dango/tool-config.yaml:ro")
	}

	args = append(args, request.Image, "dango", "executor", "run", "--task-id", request.TaskID, "--sub-task", "/etc/dango/sub-task.md")

	cmd := exec.CommandContext(ctx, d.Binary, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run executor image %q: %w: %s", request.Image, err, bytes.TrimSpace(output))
	}
	return nil
}
