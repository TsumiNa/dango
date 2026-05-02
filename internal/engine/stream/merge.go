package stream

import (
	"context"
	"errors"
	"sync"
)

// Merge connects an upstream stream to a downstream stream.
//
// The downstream stream receives each forwarded event with a new downstream
// sequence number. The original event source is preserved, while the upstream
// sequence number is copied into metadata for debugging and persistence.
type Merge struct {
	cancel context.CancelFunc
	done   chan struct{}

	errMu sync.RWMutex
	err   error
}

// MergeFrom forwards events from upstream into s until upstream closes, ctx is
// canceled, s closes, or the returned Merge is stopped.
//
// filter and opts are applied while subscribing to upstream, so a runner can
// merge only the executor/skill chunks it wants to expose without forcing all
// child-stream traffic into its own stream.
func (s *Stream) MergeFrom(ctx context.Context, upstream *Stream, filter Filter, opts ...SubscribeOption) (*Merge, error) {
	if s == nil || upstream == nil {
		return nil, ErrInvalidMerge
	}
	if s == upstream {
		return nil, ErrInvalidMerge
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sub, err := upstream.Subscribe(filter, opts...)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	merge := &Merge{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go merge.run(runCtx, s, sub)
	return merge, nil
}

func (m *Merge) run(ctx context.Context, downstream *Stream, sub *Subscription) {
	defer close(m.done)
	defer sub.Cancel()

	for {
		event, ok, err := sub.Next(ctx)
		if err != nil {
			m.setErr(err)
			return
		}
		if !ok {
			return
		}
		if err := downstream.Emit(ctx, prepareMergedEvent(event)); err != nil {
			m.setErr(err)
			return
		}
	}
}

func prepareMergedEvent(event Event) Event {
	upstreamSequence := event.SequenceNumber
	metadata := cloneMetadata(event.Metadata)
	if metadata == nil {
		metadata = make(map[string]any, 1)
	}
	metadata["upstream_sequence_number"] = upstreamSequence
	event.SequenceNumber = 0
	event.Metadata = metadata
	return event
}

// Stop detaches the merge. Done closes after the forwarding goroutine exits.
func (m *Merge) Stop() {
	if m == nil {
		return
	}
	m.cancel()
}

// Done closes when the merge stops.
func (m *Merge) Done() <-chan struct{} {
	if m == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return m.done
}

// Err reports the forwarding error, if any.
func (m *Merge) Err() error {
	if m == nil {
		return nil
	}
	m.errMu.RLock()
	defer m.errMu.RUnlock()
	return m.err
}

func (m *Merge) setErr(err error) {
	if err == nil {
		return
	}
	m.errMu.Lock()
	defer m.errMu.Unlock()
	if m.err == nil && !errors.Is(err, context.Canceled) {
		m.err = err
	}
}
