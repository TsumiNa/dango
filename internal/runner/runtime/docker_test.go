package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerCLIExecutorCommandsForwardNoHookInputs(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "docker.log")
	binary := filepath.Join(root, "fake-docker.sh")
	script := `#!/bin/sh
set -eu

printf '%s\n' '--CALL--' >> "${FAKE_DOCKER_LOG}"
for arg in "$@"; do
  printf '%s\n' "$arg" >> "${FAKE_DOCKER_LOG}"
done

printf '%s' '{"summary":"container plan","sub_task":"execute in container","expected_outputs":["result.final"]}'
`
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker binary error = %v", err)
	}
	if err := os.Chmod(binary, 0o755); err != nil {
		t.Fatalf("chmod fake docker binary error = %v", err)
	}

	t.Setenv("FAKE_DOCKER_LOG", logPath)
	t.Setenv("DANGO_LLM_BASE_URL", "https://llm.example/v1")
	t.Setenv("DANGO_LLM_MODEL", "gpt-5-test")
	t.Setenv("OPENAI_API_KEY", "sk-test")

	subTaskHost := filepath.Join(root, "sub-task.md")
	toolConfigHost := filepath.Join(root, "tool-config.yaml")
	inputHost := filepath.Join(root, "input")
	publicOutputHost := filepath.Join(root, "output")
	privateOutputHost := filepath.Join(root, "private-output")
	for _, path := range []string{subTaskHost, toolConfigHost} {
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatalf("write fixture %s error = %v", path, err)
		}
	}
	for _, dir := range []string{inputHost, publicOutputHost, privateOutputHost} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}

	cli := newDockerCLI(binary, nil)
	planPayload, err := cli.PlanExecutor(context.Background(), ExecutorPlanRequest{
		Image:          "example/no-hooks:latest",
		TaskID:         "task-123",
		SubTaskHost:    subTaskHost,
		ToolConfigHost: toolConfigHost,
	})
	if err != nil {
		t.Fatalf("PlanExecutor() error = %v", err)
	}
	if got, want := string(planPayload), `{"summary":"container plan","sub_task":"execute in container","expected_outputs":["result.final"]}`; got != want {
		t.Fatalf("PlanExecutor() payload = %q, want %q", got, want)
	}

	runRequest := ExecutorRunRequest{
		Image:             "example/no-hooks:latest",
		TaskID:            "task-123",
		SubTaskHost:       subTaskHost,
		ToolConfigHost:    toolConfigHost,
		InputHost:         inputHost,
		PublicOutputHost:  publicOutputHost,
		PrivateOutputHost: privateOutputHost,
		InputURL:          "https://input.example/task-123",
		OutputURL:         "https://output.example/task-123",
	}
	if err := cli.RunExecutor(context.Background(), runRequest); err != nil {
		t.Fatalf("RunExecutor() error = %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", logPath, err)
	}
	log := string(logBytes)
	if got, want := strings.Count(log, "--CALL--\n"), 2; got != want {
		t.Fatalf("docker invocation count = %d, want %d; log = %s", got, want, log)
	}
	for _, fragment := range []string{
		"dango\nexecutor\nplan\n",
		"dango\nexecutor\nrun\n",
		"-e\nDANGO_LLM_BASE_URL=https://llm.example/v1\n",
		"-e\nDANGO_LLM_MODEL=gpt-5-test\n",
		"-e\nOPENAI_API_KEY=sk-test\n",
		"-v\n" + subTaskHost + ":/etc/dango/sub-task.md:ro\n",
		"-v\n" + toolConfigHost + ":/etc/dango/tool-config.yaml:ro\n",
		"-e\nINPUT_PATH=" + runRequest.InputContainerPath() + "\n",
		"-e\nOUTPUT_PATH=" + runRequest.OutputContainerPath() + "\n",
		"-e\nPUBLIC_OUTPUT_PATH=" + runRequest.OutputContainerPath() + "\n",
		"-e\nPRIVATE_OUTPUT_PATH=" + runRequest.PrivateOutputContainerPath() + "\n",
		"-e\nINPUT_URL=https://input.example/task-123\n",
		"-e\nOUTPUT_URL=https://output.example/task-123\n",
		"-v\n" + inputHost + ":" + runRequest.InputContainerPath() + ":ro\n",
		"-v\n" + publicOutputHost + ":" + runRequest.OutputContainerPath() + ":rw\n",
		"-v\n" + privateOutputHost + ":" + runRequest.PrivateOutputContainerPath() + ":rw\n",
	} {
		if !strings.Contains(log, fragment) {
			t.Fatalf("docker log missing fragment %q\nfull log:\n%s", fragment, log)
		}
	}
}
