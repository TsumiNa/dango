package runtime

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"

	"github.com/tsumina/dango/internal/logging"
)

type dockerCLI struct {
	binary string
	logger *slog.Logger
}

func newDockerCLI(binary string, logger *slog.Logger) *dockerCLI {
	if binary == "" {
		binary = "docker"
	}
	return &dockerCLI{
		binary: binary,
		logger: logging.Component(logger, "runtime.docker"),
	}
}

func (d *dockerCLI) Pull(ctx context.Context, image string) error {
	d.logger.Info("pulling tool image", "image", image)
	cmd := exec.CommandContext(ctx, d.binary, "pull", image)
	output, err := cmd.CombinedOutput()
	if err != nil {
		d.logger.Error("docker pull failed", "image", image, "error", err, "output", string(bytes.TrimSpace(output)))
		return fmt.Errorf("pull image %q: %w: %s", image, err, bytes.TrimSpace(output))
	}
	d.logger.Debug("docker pull completed", "image", image)
	return nil
}

func (d *dockerCLI) DescribeTool(ctx context.Context, image string) ([]byte, error) {
	d.logger.Info("describing tool image", "image", image)
	cmd := exec.CommandContext(ctx, d.binary, "run", "--rm", image, "dango", "executor", "describe", "--format", "yaml")
	output, err := cmd.CombinedOutput()
	if err != nil {
		d.logger.Error("docker describe failed", "image", image, "error", err, "output", string(bytes.TrimSpace(output)))
		return nil, fmt.Errorf("describe tool in image %q: %w: %s", image, err, bytes.TrimSpace(output))
	}
	d.logger.Debug("tool image described", "image", image, "bytes", len(output))
	return output, nil
}

func (d *dockerCLI) PlanExecutor(ctx context.Context, request ExecutorPlanRequest) ([]byte, error) {
	d.logger.Info("planning executor container", "image", request.Image, "task_id", request.TaskID)
	args := []string{
		"run", "--rm",
		"-e", "TASK_ID=" + request.TaskID,
		"-e", "SUB_TASK=/etc/dango/sub-task.md",
		"-e", "TOOL_CONFIG=/etc/dango/tool-config.yaml",
	}
	if request.SubTaskHost != "" {
		args = append(args, "-v", request.SubTaskHost+":/etc/dango/sub-task.md:ro")
	}
	if request.ToolConfigHost != "" {
		args = append(args, "-v", request.ToolConfigHost+":/etc/dango/tool-config.yaml:ro")
	}
	args = append(args, request.Image, "dango", "executor", "plan", "--task-id", request.TaskID, "--sub-task", "/etc/dango/sub-task.md", "--format", "json")

	cmd := exec.CommandContext(ctx, d.binary, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		d.logger.Error("executor planning container failed", "image", request.Image, "task_id", request.TaskID, "error", err, "output", string(bytes.TrimSpace(output)))
		return nil, fmt.Errorf("plan executor image %q: %w: %s", request.Image, err, bytes.TrimSpace(output))
	}
	return bytes.TrimSpace(output), nil
}

func (d *dockerCLI) RunExecutor(ctx context.Context, request ExecutorRunRequest) error {
	d.logger.Info("running executor container", "image", request.Image, "task_id", request.TaskID)
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
	if request.PublicOutputHost != "" {
		args = append(args,
			"-e", "OUTPUT_PATH="+request.OutputContainerPath(),
			"-e", "PUBLIC_OUTPUT_PATH="+request.OutputContainerPath(),
			"-v", request.PublicOutputHost+":"+request.OutputContainerPath()+":rw",
		)
	}
	if request.PrivateOutputHost != "" {
		args = append(args,
			"-e", "PRIVATE_OUTPUT_PATH="+request.PrivateOutputContainerPath(),
			"-v", request.PrivateOutputHost+":"+request.PrivateOutputContainerPath()+":rw",
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

	cmd := exec.CommandContext(ctx, d.binary, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		d.logger.Error("executor container failed", "image", request.Image, "task_id", request.TaskID, "error", err, "output", string(bytes.TrimSpace(output)))
		return fmt.Errorf("run executor image %q: %w: %s", request.Image, err, bytes.TrimSpace(output))
	}
	d.logger.Info("executor container completed", "image", request.Image, "task_id", request.TaskID)
	return nil
}
