package backend

import "context"

// closeWithContext runs closeFn and bounds the wait by ctx.
//
// sql.DB.Close blocks until in-flight queries finish and pooled connections
// drain, and the standard library offers no way to interrupt it. closeWithContext
// lets a caller cap that wait with a shutdown deadline: if ctx is cancelled or
// expires first it returns ctx.Err() while closeFn keeps running to completion in
// the background (a best-effort drain, since the close itself cannot be aborted).
// A nil ctx waits for closeFn indefinitely.
func closeWithContext(ctx context.Context, closeFn func() error) error {
	if ctx == nil {
		return closeFn()
	}
	done := make(chan error, 1) // buffered so the goroutine never blocks if ctx wins
	go func() { done <- closeFn() }()
	// No default arm: block until the close finishes or ctx is done. If both are
	// ready at once Go picks an arm at random, and returning ctx.Err() is the
	// intended outcome for a deadline-bounded shutdown.
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
