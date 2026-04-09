package orchestrator

import (
	"context"
	"time"
)

const gracefulShutdownTimeout = 5 * time.Second

func finalizeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), gracefulShutdownTimeout)
}
