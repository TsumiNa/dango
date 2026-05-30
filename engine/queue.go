package engine

import (
	"container/heap"
	"context"
	"sync"

	runnerpkg "github.com/tsumina/dango/engine/runner"
)

func (o *Orchestrator) submitManagedRunner(ctx context.Context, runner *runnerpkg.Runner, priority RequestPriority) error {
	ctx = o.operationContext(ctx)
	if err := ctx.Err(); err != nil {
		runner.Abort(err)
		return err
	}

	id := runner.ID()
	o.mu.Lock()
	if o.maxRunningRunners == 0 || len(o.runningRunnerIDs) < o.maxRunningRunners {
		if o.maxRunningRunners > 0 {
			o.runningRunnerIDs[id] = struct{}{}
		}
		o.mu.Unlock()
		return o.startManagedRunner(ctx, runner)
	}
	entry := &queuedRunner{
		runner:   runner,
		ctx:      ctx,
		priority: priority,
		order:    o.nextQueueOrder,
		done:     make(chan struct{}),
	}
	o.nextQueueOrder++
	heap.Push(&o.queuedRunners, entry)
	o.queuedRunnerByID[id] = entry
	o.mu.Unlock()

	go o.watchQueuedRunner(entry)
	return nil
}

func (o *Orchestrator) watchQueuedRunner(entry *queuedRunner) {
	select {
	case <-entry.done:
		return
	case <-entry.ctx.Done():
		o.cancelQueuedRunner(entry, entry.ctx.Err())
	}
}

func (o *Orchestrator) cancelQueuedRunner(entry *queuedRunner, runErr error) {
	toStart, toCancel := o.removeQueuedRunner(entry, true)
	entry.runner.Abort(runErr)
	o.finishQueuedDispatch(toStart, toCancel)
}

func (o *Orchestrator) removeQueuedRunner(entry *queuedRunner, canceled bool) ([]*queuedRunner, []*queuedRunner) {
	id := entry.runner.ID()
	o.mu.Lock()
	current := o.queuedRunnerByID[id]
	if current != entry {
		o.mu.Unlock()
		return nil, nil
	}
	if canceled {
		entry.canceled = true
	}
	delete(o.queuedRunnerByID, id)
	entry.deactivate()
	toStart, toCancel := o.collectQueuedStartsLocked()
	o.mu.Unlock()
	return toStart, toCancel
}

func (o *Orchestrator) startManagedRunner(ctx context.Context, runner *runnerpkg.Runner) error {
	ctx = o.operationContext(ctx)
	id := runner.ID()
	o.mu.Lock()
	if entry := o.queuedRunnerByID[id]; entry != nil {
		entry.canceled = true
		delete(o.queuedRunnerByID, id)
		entry.deactivate()
	}
	o.mu.Unlock()
	err := runner.StartManaged(ctx)
	if err != nil {
		o.mu.Lock()
		delete(o.runningRunnerIDs, id)
		toStart, toCancel := o.collectQueuedStartsLocked()
		o.mu.Unlock()
		if runner.State().Status == runnerpkg.RunnerStatusPending {
			runner.Abort(err)
		}
		o.finishQueuedDispatch(toStart, toCancel)
		return err
	}
	return nil
}

func (o *Orchestrator) releaseRunnerExecutionSlot(id string) {
	o.mu.Lock()
	delete(o.runningRunnerIDs, id)
	toStart, toCancel := o.collectQueuedStartsLocked()
	o.mu.Unlock()
	o.finishQueuedDispatch(toStart, toCancel)
}

func (o *Orchestrator) collectQueuedStartsLocked() ([]*queuedRunner, []*queuedRunner) {
	var toStart []*queuedRunner
	var toCancel []*queuedRunner
	for o.maxRunningRunners == 0 || len(o.runningRunnerIDs) < o.maxRunningRunners {
		entry := o.popQueuedRunnerLocked()
		if entry == nil {
			break
		}
		delete(o.queuedRunnerByID, entry.runner.ID())
		entry.deactivate()
		if entry.canceled {
			continue
		}
		if err := entry.ctx.Err(); err != nil {
			entry.canceled = true
			toCancel = append(toCancel, entry)
			continue
		}
		if o.maxRunningRunners > 0 {
			o.runningRunnerIDs[entry.runner.ID()] = struct{}{}
		}
		toStart = append(toStart, entry)
	}
	return toStart, toCancel
}

func (o *Orchestrator) popQueuedRunnerLocked() *queuedRunner {
	for len(o.queuedRunners) > 0 {
		entry := heap.Pop(&o.queuedRunners).(*queuedRunner)
		if entry.canceled {
			continue
		}
		return entry
	}
	return nil
}

func (o *Orchestrator) finishQueuedDispatch(toStart []*queuedRunner, toCancel []*queuedRunner) {
	for _, entry := range toCancel {
		entry.runner.Abort(entry.ctx.Err())
	}
	for _, entry := range toStart {
		_ = o.startManagedRunner(entry.ctx, entry.runner)
	}
}

type queuedRunner struct {
	runner   *runnerpkg.Runner
	ctx      context.Context
	priority RequestPriority
	order    uint64
	canceled bool
	done     chan struct{}
	doneOnce sync.Once
}

func (q *queuedRunner) deactivate() {
	q.doneOnce.Do(func() {
		close(q.done)
	})
}

type runnerStartQueue []*queuedRunner

func (q runnerStartQueue) Len() int { return len(q) }

func (q runnerStartQueue) Less(i, j int) bool {
	if q[i].priority == q[j].priority {
		return q[i].order < q[j].order
	}
	return q[i].priority > q[j].priority
}

func (q runnerStartQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
}

func (q *runnerStartQueue) Push(x any) {
	*q = append(*q, x.(*queuedRunner))
}

func (q *runnerStartQueue) Pop() any {
	old := *q
	n := len(old)
	entry := old[n-1]
	*q = old[:n-1]
	return entry
}
