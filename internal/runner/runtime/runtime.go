package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
)

// ContainerRuntime describes the execution backend contract shared by the
// orchestrator and runner.
//
// Implementations are responsible for making a tool reference available,
// describing its tool spec, refining executor detail plans, and running one
// executor invocation. Higher-level packages should depend on this interface so
// planning and execution stay independent from whether a tool runs through
// Docker or directly on the host.
type ContainerRuntime interface {
	// Pull ensures the runtime can execute image.
	Pull(ctx context.Context, image string) error
	// DescribeTool returns the tool specification payload for image.
	DescribeTool(ctx context.Context, image string) ([]byte, error)
	// PlanExecutor refines one task edge during the planning phase.
	PlanExecutor(ctx context.Context, request ExecutorPlanRequest) ([]byte, error)
	// RunExecutor executes one tool invocation described by request.
	RunExecutor(ctx context.Context, request ExecutorRunRequest) error
}

// ExecutorPlanRequest captures the host-side inputs needed to refine one edge.
//
// The runner constructs this request after it has written the current sub-task
// markdown and merged tool configuration to disk. Runtime implementations map
// these host paths into executor-visible inputs for the detail-planning phase.
type ExecutorPlanRequest struct {
	// Image identifies the tool image or host tool reference to execute.
	Image string
	// TaskID scopes the planning request to a single task lineage.
	TaskID string
	// SubTaskHost points to the host planning markdown file.
	SubTaskHost string
	// ToolConfigHost points to the merged host tool configuration file.
	ToolConfigHost string
}

// ExecutorRunRequest captures the host-side inputs needed to execute a tool.
//
// The scheduler constructs this request after it has resolved upstream input
// directories and created the public and private output roots for the edge.
// Runtime implementations translate this structure into mounts, environment
// variables, or remote URLs understood by the executor contract.
type ExecutorRunRequest struct {
	// Image identifies the tool image or host tool reference to execute.
	Image string
	// TaskID scopes mounts and environment variables for a single task run.
	TaskID string
	// SubTaskHost points to the host sub-task markdown file.
	SubTaskHost string
	// ToolConfigHost points to the merged host tool configuration file.
	ToolConfigHost string
	// InputHost points to the host input directory when local storage is used.
	InputHost string
	// PublicOutputHost points to the host directory for orchestrator-visible artifacts.
	PublicOutputHost string
	// PrivateOutputHost points to the host directory for downstream-only artifacts.
	PrivateOutputHost string
	// InputURL points to the remote input payload when URL-based storage is used.
	InputURL string
	// OutputURL points to the remote output destination when URL-based storage is used.
	OutputURL string
}

// InputContainerPath returns the canonical in-container mount path for input
// artifacts.
//
// Container backends use this when wiring INPUT_PATH for executor processes.
func (r ExecutorRunRequest) InputContainerPath() string {
	return path.Join("/mnt/shared", r.TaskID, "input")
}

// OutputContainerPath returns the canonical in-container mount path for public
// output artifacts.
//
// Container backends use this when wiring PUBLIC_OUTPUT_PATH or OUTPUT_PATH for
// executor processes.
func (r ExecutorRunRequest) OutputContainerPath() string {
	return path.Join("/mnt/shared", r.TaskID, "output")
}

// PrivateOutputContainerPath returns the canonical in-container mount path for
// downstream-only output artifacts.
//
// Container backends use this when wiring PRIVATE_OUTPUT_PATH for executor
// processes.
func (r ExecutorRunRequest) PrivateOutputContainerPath() string {
	return path.Join("/mnt/shared", r.TaskID, "_output")
}

// MultiRuntime dispatches runtime operations to Docker or host-local execution
// based on the tool reference.
//
// MultiRuntime is the default production implementation of ContainerRuntime.
// References prefixed with HostPrefix are sent to the host runtime, while all
// other references are delegated to the Docker runtime.
type MultiRuntime struct {
	docker ContainerRuntime
	host   ContainerRuntime
}

// NewDefault constructs the default runtime multiplexer used by the
// orchestrator.
//
// dockerBinary defaults to "docker" when empty. The returned runtime wires up
// both the Docker backend and the host-local backend so callers can stay
// agnostic about where a tool runs.
func NewDefault(dockerBinary string, logger *slog.Logger) *MultiRuntime {
	return &MultiRuntime{
		docker: newDockerCLI(dockerBinary, logger),
		host:   newHostRuntime(logger),
	}
}

// Pull resolves the appropriate backend and ensures the tool reference is ready
// for later describe, plan, or run operations.
func (m *MultiRuntime) Pull(ctx context.Context, image string) error {
	rt, err := m.resolve(image)
	if err != nil {
		return err
	}
	return rt.Pull(ctx, image)
}

// DescribeTool resolves the backend and returns the described tool spec
// payload.
func (m *MultiRuntime) DescribeTool(ctx context.Context, image string) ([]byte, error) {
	rt, err := m.resolve(image)
	if err != nil {
		return nil, err
	}
	return rt.DescribeTool(ctx, image)
}

// PlanExecutor resolves the backend and runs executor-side detail planning for
// the requested edge.
func (m *MultiRuntime) PlanExecutor(ctx context.Context, request ExecutorPlanRequest) ([]byte, error) {
	rt, err := m.resolve(request.Image)
	if err != nil {
		return nil, err
	}
	return rt.PlanExecutor(ctx, request)
}

// RunExecutor resolves the backend and executes the requested tool invocation.
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
