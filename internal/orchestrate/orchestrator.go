package orchestrate

import (
	"log/slog"
)

type Orchestrator struct {
	// Add fields for managing state, configuration, etc.
	logger  *slog.Logger
	runtime *Runtime
}

type Request struct {
	// Define the structure of the request that will be dispatched to workers.

}

func NewOrchestrator(logger *slog.Logger) *Orchestrator {
	return &Orchestrator{
		logger:  logger,
		runtime: NewRuntime(logger),
	}
}
