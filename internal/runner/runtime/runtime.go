package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"
)

// ContainerRuntime describes the minimum runtime behavior needed by the
// orchestrator to register and execute tools.
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

// InputContainerPath returns the path exposed to a container for mounted input.
func (r ExecutorRunRequest) InputContainerPath() string {
	return path.Join("/mnt/shared", r.TaskID, "input")
}

// OutputContainerPath returns the path exposed to a container for mounted output.
func (r ExecutorRunRequest) OutputContainerPath() string {
	return path.Join("/mnt/shared", r.TaskID, "output")
}

// PrivateOutputContainerPath returns the private path exposed to a container for mounted output.
func (r ExecutorRunRequest) PrivateOutputContainerPath() string {
	return path.Join("/mnt/shared", r.TaskID, "_output")
}

// MultiRuntime dispatches tool actions to either the Docker runtime or the
// host-local runtime based on the image reference scheme.
type MultiRuntime struct {
	docker ContainerRuntime
	host   ContainerRuntime
}

// NewDefault constructs the default runtime multiplexer used by the
// orchestrator.
//
// dockerBinary defaults to "docker" when empty.
func NewDefault(dockerBinary string, logger *slog.Logger) *MultiRuntime {
	return &MultiRuntime{
		docker: newDockerCLI(dockerBinary, logger),
		host:   newHostRuntime(logger),
	}
}

// Pull resolves the appropriate backend and ensures image is available.
func (m *MultiRuntime) Pull(ctx context.Context, image string) error {
	rt, err := m.resolve(image)
	if err != nil {
		return err
	}
	return rt.Pull(ctx, image)
}

// DescribeTool resolves the backend and returns the tool description payload.
func (m *MultiRuntime) DescribeTool(ctx context.Context, image string) ([]byte, error) {
	rt, err := m.resolve(image)
	if err != nil {
		return nil, err
	}
	return rt.DescribeTool(ctx, image)
}

// PlanExecutor resolves the backend and refines the requested tool edge.
func (m *MultiRuntime) PlanExecutor(ctx context.Context, request ExecutorPlanRequest) ([]byte, error) {
	rt, err := m.resolve(request.Image)
	if err != nil {
		return nil, err
	}
	return rt.PlanExecutor(ctx, request)
}

// RunExecutor resolves the backend and runs the requested tool invocation.
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
