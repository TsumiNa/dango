package runner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tsumina/dango/internal/logging"
	"github.com/tsumina/dango/internal/spec"
)

// EdgeResult reports the terminal result of one completed edge execution.
//
// The state machine returns these values in completion order so TaskRunner can
// derive terminal handoffs and build the final task result.
type EdgeResult struct {
	// EdgeID identifies the completed edge.
	EdgeID string
	// ToolName identifies the tool assigned to the edge.
	ToolName string
	// Handoff is the parsed completion handoff emitted by the executor.
	Handoff spec.Handoff
}

type edgeExecutionOutcome struct {
	result EdgeResult
	err    error
}

// StateMachine supervises concurrent DAG execution for one planned task.
//
// StateMachine is responsible for dependency tracking and launch ordering, not
// for the actual executor invocation. It depends on [Scheduler] to run one edge
// at a time and then uses a channel-driven loop to launch newly ready edges as
// upstream work completes.
type StateMachine struct {
	scheduler *Scheduler
	logger    *slog.Logger
}

// NewStateMachine constructs the DAG execution supervisor used by TaskRunner.
//
// The returned state machine is intended for one task run at a time.
func NewStateMachine(scheduler *Scheduler, logger *slog.Logger) *StateMachine {
	return &StateMachine{
		scheduler: scheduler,
		logger:    logging.Component(logger, "runner.state_machine"),
	}
}

// Run executes the DAG until all edges complete or any edge fails.
//
// Run keeps three pieces of state: pending edges that have not launched,
// running edges currently owned by the scheduler, and completed edges whose
// handoffs may unblock downstream work. As soon as a dependency set becomes
// satisfied, Run launches the edge in a goroutine. The method returns the edge
// results in completion order, cancels remaining work on the first failure, and
// reports a deadlock when no runnable edges remain before the DAG is complete.
func (m *StateMachine) Run(ctx context.Context, taskID string, plan spec.DAGPlan) ([]EdgeResult, error) {
	if len(plan.Edges) == 0 {
		return nil, nil
	}
	if m.scheduler == nil {
		return nil, fmt.Errorf("runner scheduler is required")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	outcomes := make(chan edgeExecutionOutcome, len(plan.Edges))
	pending := make(map[string]spec.PlannedEdge, len(plan.Edges))
	completed := make(map[string]EdgeResult, len(plan.Edges))
	running := make(map[string]bool, len(plan.Edges))
	ordered := make([]EdgeResult, 0, len(plan.Edges))

	for _, edge := range plan.Edges {
		pending[edge.ID] = edge
	}

	launchReadyEdges := func() {
		for _, edge := range plan.Edges {
			if _, ok := pending[edge.ID]; !ok {
				continue
			}
			if running[edge.ID] || !dependenciesSatisfied(edge.Dependencies, completed) {
				continue
			}

			running[edge.ID] = true
			delete(pending, edge.ID)
			edgeCopy := edge
			go func() {
				handoff, err := m.scheduler.RunLocalEdge(runCtx, EdgeExecutionRequest{
					TaskID:            taskID,
					EdgeID:            edgeCopy.ID,
					ToolName:          edgeCopy.ToolName,
					DependencyEdgeIDs: append([]string(nil), edgeCopy.Dependencies...),
					SubTaskContent:    edgeCopy.SubTask,
				})
				outcomes <- edgeExecutionOutcome{
					result: EdgeResult{EdgeID: edgeCopy.ID, ToolName: edgeCopy.ToolName, Handoff: handoff},
					err:    err,
				}
			}()
		}
	}

	launchReadyEdges()

	for len(completed) < len(plan.Edges) {
		if len(running) == 0 {
			return nil, fmt.Errorf("runner execution deadlock: no runnable edges remain for task %s", taskID)
		}

		select {
		case <-runCtx.Done():
			if len(running) == 0 {
				return nil, runCtx.Err()
			}
			continue
		case outcome := <-outcomes:
			delete(running, outcome.result.EdgeID)
			if outcome.err != nil {
				cancel()
				m.logger.Error("edge execution failed", "task_id", taskID, "edge_id", outcome.result.EdgeID, "tool", outcome.result.ToolName, "error", outcome.err)
				return nil, outcome.err
			}

			completed[outcome.result.EdgeID] = outcome.result
			ordered = append(ordered, outcome.result)
			launchReadyEdges()
		}
	}

	return ordered, nil
}

func dependenciesSatisfied(dependencies []string, completed map[string]EdgeResult) bool {
	for _, dependency := range dependencies {
		dependency = strings.TrimSpace(dependency)
		if dependency == "" {
			continue
		}
		if _, ok := completed[dependency]; !ok {
			return false
		}
	}
	return true
}
