package runner

import (
	"context"
	"fmt"
	"time"
)

func (r *Runner) transitionState(status RunnerStatus, runErr error, terminal bool) (RunnerState, bool) {
	now := time.Now()
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}

	r.stateMu.Lock()
	defer r.stateMu.Unlock()

	next := r.state
	if next.Status == status && next.Error == errText && (!terminal || !next.FinishedAt.IsZero()) {
		return next, false
	}
	if next.StartedAt.IsZero() && status != RunnerStatusPending {
		next.StartedAt = now
	}
	next.Status = status
	next.UpdatedAt = now
	next.Error = errText
	if terminal {
		next.FinishedAt = now
	}
	r.state = next
	return next, true
}

func (r *Runner) appendRecord(store RunnerStore, rec *RunnerRecord) error {
	if store == nil {
		return nil
	}
	if _, err := store.Append(context.Background(), r.id, rec); err != nil {
		return fmt.Errorf("orchestrate: persist runner %q: %w", r.id, err)
	}
	return nil
}

func (r *Runner) recordState(store RunnerStore, status RunnerStatus, runErr error, terminal bool) error {
	state, changed := r.transitionState(status, runErr, terminal)
	if !changed {
		return nil
	}
	return r.appendRecord(store, &RunnerRecord{Kind: RunnerRecordStatus, Status: state.Status, Error: state.Error})
}
