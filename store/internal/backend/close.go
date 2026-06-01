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
	done := make(chan error, 1)
	go func() { done <- closeFn() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
