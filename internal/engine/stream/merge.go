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

// upstreamIdentity uniquely identifies one upstream stream within a merge.
// It combines the Layer and ID from the event Source.
type upstreamIdentity struct {
	layer string
	id    string
}

// joinKey uniquely identifies a joinable group within a merge tick.
// It combines the upstream identity, EventType, and Status.
type joinKey struct {
	upstream  upstreamIdentity
	eventType string
	status    string
}

// upstreamIdentityOf extracts the upstream identity from an event source.
func upstreamIdentityOf(src Source) upstreamIdentity {
	return upstreamIdentity{
		layer: src.Layer,
		id:    src.ID,
	}
}

// joinKeyOf extracts the join key from an event.
// The join key is used to determine if multiple events can be combined into
// a single joined delta within the same tick.
func joinKeyOf(event Event) joinKey {
	return joinKey{
		upstream:  upstreamIdentityOf(event.From),
		eventType: event.EventType,
		status:    event.Status,
	}
}

// isJoinableStringDelta reports whether a delta is a JSON string that can be
// safely joined with other string deltas.
// Only JSON strings (not objects, arrays, numbers, booleans, or null) are
// joinable, as they represent append-safe streaming content like LLM output.
func isJoinableStringDelta(delta []byte) bool {
	if len(delta) < 2 {
		return false
	}
	// JSON strings start with '"' and end with '"'
	return delta[0] == '"' && delta[len(delta)-1] == '"'
}

// canJoinDeltas reports whether two consecutive deltas with the same join key
// can be combined into a single joined delta.
// Both deltas must be JSON strings to be joinable.
func canJoinDeltas(prevDelta, nextDelta []byte) bool {
	return isJoinableStringDelta(prevDelta) && isJoinableStringDelta(nextDelta)
}

// ErrBufferFull is returned when an upstreamFIFO cannot accept more events
// because it has reached its maximum depth.
var ErrBufferFull = errors.New("stream: FIFO buffer full")

// upstreamFIFO buffers events from a single upstream within a merge hub.
//
// It preserves per-upstream FIFO order and supports same-tick join attempts
// for consecutive events with the same join key and joinable string deltas.
type upstreamFIFO struct {
	// identity uniquely identifies this upstream.
	identity upstreamIdentity

	// maxDepth limits the number of queued events to prevent unbounded growth.
	// When full, enqueue returns ErrBufferFull.
	maxDepth int

	// queue holds pending events in FIFO order.
	// events[0] is the head (next to be consumed).
	events []Event
}

// newUpstreamFIFO creates a new FIFO for the given upstream with a depth limit.
// maxDepth must be positive; if not, a minimum depth of 1000 is used.
func newUpstreamFIFO(identity upstreamIdentity, maxDepth int) *upstreamFIFO {
	if maxDepth <= 0 {
		maxDepth = 1000
	}
	return &upstreamFIFO{
		identity: identity,
		maxDepth: maxDepth,
		events:   make([]Event, 0, maxDepth),
	}
}

// enqueue adds an event to the FIFO tail.
// Returns ErrBufferFull if the FIFO is at capacity.
func (f *upstreamFIFO) enqueue(event Event) error {
	if len(f.events) >= f.maxDepth {
		return ErrBufferFull
	}
	f.events = append(f.events, event)
	return nil
}

// peek returns the event at the head of the FIFO without removing it.
// Returns nil and false if the FIFO is empty.
func (f *upstreamFIFO) peek() (Event, bool) {
	if len(f.events) == 0 {
		return Event{}, false
	}
	return f.events[0], true
}

// pop removes and returns the event at the head of the FIFO.
// Returns an empty event and false if the FIFO is empty.
func (f *upstreamFIFO) pop() (Event, bool) {
	if len(f.events) == 0 {
		return Event{}, false
	}
	event := f.events[0]
	f.events = f.events[1:]
	return event, true
}

// len returns the number of events currently in the FIFO.
func (f *upstreamFIFO) len() int {
	return len(f.events)
}

// tryJoinAtHead attempts to join the second event with the first event at the
// FIFO head by combining their string deltas.
// If both events have the same join key and both have joinable string deltas,
// the deltas are combined (merged as JSON strings) and the first event is
// updated with the combined delta, followed by removal of the second event.
// Returns true if join succeeded, false otherwise.
// The FIFO must have at least one event; behavior is undefined if empty.
func (f *upstreamFIFO) tryJoinAtHead(nextEvent Event) bool {
	if len(f.events) < 1 {
		return false
	}

	head := &f.events[0]
	headKey := joinKeyOf(*head)
	nextKey := joinKeyOf(nextEvent)

	// Different join keys cannot be joined.
	if headKey != nextKey {
		return false
	}

	// Only joinable string deltas can be combined.
	if !canJoinDeltas(head.Delta, nextEvent.Delta) {
		return false
	}

	// Combine the deltas as JSON strings:
	// Remove quotes from both strings and merge the content.
	// "hello" + " world" => "hello world"
	prevStr := head.Delta[1 : len(head.Delta)-1]
	nextStr := nextEvent.Delta[1 : len(nextEvent.Delta)-1]
	combined := append([]byte(nil), append([]byte(`"`), append(prevStr, append(nextStr, '"')...)...)...)
	head.Delta = combined

	return true
}
