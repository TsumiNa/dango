package executor

import (
	"fmt"
	"os"
	"strings"
)

type runtimeContext struct {
	TaskID            string
	SubTaskPath       string
	ToolConfigPath    string
	InputPath         string
	PublicOutputPath  string
	PrivateOutputPath string
	InputURL          string
	OutputURL         string
}

func loadPlanContext(options PlanOptions) (runtimeContext, error) {
	taskID := firstNonEmpty(options.TaskID, os.Getenv("TASK_ID"))
	if strings.TrimSpace(taskID) == "" {
		return runtimeContext{}, fmt.Errorf("task id is required via --task-id or TASK_ID")
	}

	subTaskPath := firstNonEmpty(options.SubTask, os.Getenv("SUB_TASK"))
	if strings.TrimSpace(subTaskPath) == "" {
		return runtimeContext{}, fmt.Errorf("sub-task path is required via --sub-task or SUB_TASK")
	}

	return runtimeContext{
		TaskID:            taskID,
		SubTaskPath:       subTaskPath,
		ToolConfigPath:    strings.TrimSpace(os.Getenv("TOOL_CONFIG")),
		InputPath:         strings.TrimSpace(os.Getenv("INPUT_PATH")),
		PublicOutputPath:  strings.TrimSpace(os.Getenv("PUBLIC_OUTPUT_PATH")),
		PrivateOutputPath: strings.TrimSpace(os.Getenv("PRIVATE_OUTPUT_PATH")),
		InputURL:          strings.TrimSpace(os.Getenv("INPUT_URL")),
		OutputURL:         strings.TrimSpace(os.Getenv("OUTPUT_URL")),
	}, nil
}

func loadRunContext(options RunOptions) (runtimeContext, error) {
	ctx, err := loadPlanContext(PlanOptions{
		TaskID:  options.TaskID,
		SubTask: options.SubTask,
	})
	if err != nil {
		return runtimeContext{}, err
	}

	publicOutputPath := firstNonEmpty(ctx.PublicOutputPath, strings.TrimSpace(os.Getenv("OUTPUT_PATH")))
	if strings.TrimSpace(publicOutputPath) == "" {
		return runtimeContext{}, fmt.Errorf("PUBLIC_OUTPUT_PATH or OUTPUT_PATH is required")
	}

	privateOutputPath := strings.TrimSpace(os.Getenv("PRIVATE_OUTPUT_PATH"))
	if privateOutputPath == "" {
		return runtimeContext{}, fmt.Errorf("PRIVATE_OUTPUT_PATH is required")
	}

	ctx.PublicOutputPath = publicOutputPath
	ctx.PrivateOutputPath = privateOutputPath
	return ctx, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
