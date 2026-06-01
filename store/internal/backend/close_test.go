package backend

import (
	"context"
	"errors"
	"testing"
)

func TestCloseWithContext_ReturnsCloseResult(t *testing.T) {
	t.Parallel()

	if err := closeWithContext(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("close (nil result) = %v, want nil", err)
	}

	boom := errors.New("boom")
	if err := closeWithContext(context.Background(), func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("close error = %v, want boom", err)
	}
}

func TestCloseWithContext_HonorsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// closeFn blocks until the test releases it, so the only select case that
	// can fire is ctx.Done() — making the deadline-bounded path deterministic.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	err := closeWithContext(ctx, func() error {
		<-release
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestCloseWithContext_NilContextRunsClose(t *testing.T) {
	t.Parallel()

	called := false
	if err := closeWithContext(nil, func() error { called = true; return nil }); err != nil {
		t.Fatalf("nil ctx close = %v, want nil", err)
	}
	if !called {
		t.Fatal("closeFn was not invoked for a nil context")
	}
}
