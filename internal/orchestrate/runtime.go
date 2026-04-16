package orchestrate

import (
	"log/slog"

	"github.com/lithammer/shortuuid/v4"
)

type Runtime struct {
	// Add fields for managing runtime state, configuration, etc.
	id string

	logger slog.Logger
}

func NewRuntime(logger *slog.Logger) *Runtime {
	return &Runtime{
		// Initialize fields as necessary.
		id:     shortuuid.NewWithNamespace("http://github.com/tsumina/dango"),
		logger: *logger,
	}
}

// Add methods to manage the runtime, execute tasks, handle results, etc.
func (e *Runtime) ExecuteNext() error {
	// Implement the logic to execute a task, manage state, handle results, etc.
	e.logger.Info("Executing the next task...")
	return nil
}

// AddTask adds a new task to the runtime.
// The previousTask parameter let [Runtime] know how to link a previous one to the new one, so it can manage the execution flow.
// The shared parameter is used to share data with file path or S3-like url between tasks.
// [ExecuteNext] will execute the next task in the runtime, and it can access the shared data through the share parameter.
func (e *Runtime) AddTask(newTask any, previousTaskId uint32, shared ...SharedData) error {
	e.logger.Info("Adding a new task...")
	return nil
}
