package executor

import (
	"fmt"
	"os"
	"strings"
)

type runtimeContext struct {
	TaskID         string
	SubTaskPath    string
	ToolConfigPath string
	InputPath      string
	OutputPath     string
	InputURL       string
	OutputURL      string
}

func loadRuntimeContext(options RunOptions) (runtimeContext, error) {
	taskID := firstNonEmpty(options.TaskID, os.Getenv("TASK_ID"))
	if strings.TrimSpace(taskID) == "" {
		return runtimeContext{}, fmt.Errorf("task id is required via --task-id or TASK_ID")
	}

	subTaskPath := firstNonEmpty(options.SubTask, os.Getenv("SUB_TASK"))
	if strings.TrimSpace(subTaskPath) == "" {
		return runtimeContext{}, fmt.Errorf("sub-task path is required via --sub-task or SUB_TASK")
	}

	outputPath := strings.TrimSpace(os.Getenv("OUTPUT_PATH"))
	if outputPath == "" {
		return runtimeContext{}, fmt.Errorf("OUTPUT_PATH is required")
	}

	return runtimeContext{
		TaskID:         taskID,
		SubTaskPath:    subTaskPath,
		ToolConfigPath: strings.TrimSpace(os.Getenv("TOOL_CONFIG")),
		InputPath:      strings.TrimSpace(os.Getenv("INPUT_PATH")),
		OutputPath:     outputPath,
		InputURL:       strings.TrimSpace(os.Getenv("INPUT_URL")),
		OutputURL:      strings.TrimSpace(os.Getenv("OUTPUT_URL")),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
