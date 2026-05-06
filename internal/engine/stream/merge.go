package stream

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Merge connects an upstream stream to a downstream stream.
//
// The downstream stream receives each forwarded event with a new downstream
// sequence number. The original event source is preserved, while the upstream
// sequence number is copied into metadata for debugging and persistence.
//
// When MergeWindowConfig has TickDuration > 0, events are collected into bundles
// by the merge hub. When TickDuration is 0 (default), events are forwarded directly.
type Merge struct {
	cancel context.CancelFunc
	done   chan struct{}

	errMu sync.RWMutex
	err   error

	// hub is non-nil when hub mode is enabled via MergeWindowConfig.
	hub *mergeHub

	// hubIdentity identifies this upstream within the hub.
	hubIdentity upstreamIdentity
}

// MergeFrom forwards events from upstream into s until upstream closes, ctx is
// canceled, s closes, or the returned Merge is stopped.
//
// filter and opts are applied while subscribing to upstream, so a runner can
// merge only the executor/skill chunks it wants to expose without forcing all
// child-stream traffic into its own stream.
//
// MergeFrom uses direct forwarding (TickDuration = 0). Use MergeFromWithConfig
// to enable hub mode with tick-based bundling.
func (s *Stream) MergeFrom(ctx context.Context, upstream *Stream, filter Filter, opts ...SubscribeOption) (*Merge, error) {
	return s.MergeFromWithConfig(ctx, upstream, filter, DefaultMergeWindowConfig(), opts...)
}

// MergeFromWithConfig forwards events from upstream into s with configurable
// hub behavior. When config.TickDuration > 0, events are collected into bundles
// by a merge hub. When TickDuration is 0, uses direct forwarding (same as MergeFrom).
//
// filter and opts are applied while subscribing to upstream.
func (s *Stream) MergeFromWithConfig(ctx context.Context, upstream *Stream, filter Filter, config MergeWindowConfig, opts ...SubscribeOption) (*Merge, error) {
	if s == nil || upstream == nil {
		return nil, ErrInvalidMerge
	}
	if s == upstream {
		return nil, ErrInvalidMerge
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Subscribe to upstream first, regardless of mode.
	sub, err := upstream.Subscribe(filter, opts...)
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	merge := &Merge{
		cancel: cancel,
		done:   make(chan struct{}),
	}

	// If hub mode is disabled (TickDuration == 0), use direct forwarding.
	if config.TickDuration == 0 {
		go merge.runDirect(runCtx, s, sub)
		return merge, nil
	}

	// Hub mode: create hub, read first event to determine upstream identity,
	// then add upstream to hub.
	hub := newMergeHub(runCtx, s, config.TickDuration, config.PerUpstreamBufferDepth)
	merge.hub = hub

	// Start a goroutine to handle hub initialization and feeding.
	go merge.runHubWithFirstEvent(runCtx, hub, sub)

	return merge, nil
}

// runHubWithFirstEvent reads the first event from the subscription to determine
// the upstream identity, registers it with the hub, and then feeds events.
func (m *Merge) runHubWithFirstEvent(ctx context.Context, hub *mergeHub, sub *Subscription) {
	defer close(m.done)
	defer sub.Cancel()

	// Read the first event to determine upstream identity.
	firstEvent, ok, err := sub.Next(ctx)
	if err != nil {
		m.setErr(err)
		return
	}
	if !ok {
		// Subscription closed without any events.
		return
	}

	// Extract identity from the first event's source.
	identity := upstreamIdentityOf(firstEvent.From)
	m.hubIdentity = identity

	// Create a temporary buffer to hold the first event.
	// We'll feed it to the hub after registering the upstream.
	tempEvent := firstEvent

	// At this point, the hub is running but this upstream is not yet registered.
	// We need to register it before feeding events. But we can't call addUpstream
	// here because addUpstream spawns a goroutine that reads from a subscription.
	// Instead, we'll manually manage the FIFO and feed loop.

	// Actually, let's use a different approach: create a small buffer that we'll
	// feed the first event into, then call addUpstream with a custom subscription.
	eventChan := make(chan Event, 1)
	eventChan <- tempEvent

	// We can't directly call addUpstream because it expects a Subscription, not a channel.
	// Instead, let's register the FIFO manually and then feed events.

	// Register FIFO in hub for this upstream.
	hub.mu.Lock()
	fifo := newUpstreamFIFO(identity, hub.perUpstreamBufferDepth)
	hub.fifosByIdentity[identity] = fifo
	hub.mu.Unlock()

	// Feed the first event and subsequent events from subscription.
	if fifo.len() > 0 && fifo.tryJoinAtHead(tempEvent) {
		// Successfully joined - don't enqueue.
	} else {
		if err := fifo.enqueue(tempEvent); err != nil {
			m.setErr(err)
			return
		}
	}

	// Feed remaining events.
	for {
		select {
		case <-ctx.Done():
			return
		case <-hub.ctx.Done():
			return
		default:
		}

		event, ok, err := sub.Next(ctx)
		if err != nil {
			m.setErr(err)
			return
		}
		if !ok {
			// Subscription closed.
			hub.mu.Lock()
			delete(hub.fifosByIdentity, identity)
			hub.mu.Unlock()
			return
		}

		// Try to join with the head event if the FIFO is not empty.
		if fifo.len() > 0 && fifo.tryJoinAtHead(event) {
			// Successfully joined - event was not added to FIFO.
			continue
		}

		// Enqueue event into FIFO.
		if err := fifo.enqueue(event); err != nil {
			m.setErr(err)
			return
		}
	}
}

// runDirect is the direct forwarding loop (used when TickDuration == 0).
func (m *Merge) runDirect(ctx context.Context, downstream *Stream, sub *Subscription) {
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
	if m.hub != nil {
		m.hub.Stop()
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

// MergeWindowConfig controls hub behavior for tick-based merging.
type MergeWindowConfig struct {
	// TickDuration is the time window for collecting events into a single bundle.
	// When zero, hub mode is disabled and direct forwarding is used.
	TickDuration time.Duration

	// PerUpstreamBufferDepth limits events queued per upstream before overflow.
	// When zero, a default of 1000 is used.
	PerUpstreamBufferDepth int
}

// DefaultMergeWindowConfig returns a MergeWindowConfig with direct-forwarding
// behavior (TickDuration = 0).
func DefaultMergeWindowConfig() MergeWindowConfig {
	return MergeWindowConfig{
		TickDuration:           0,
		PerUpstreamBufferDepth: 1000,
	}
}

// mergeHub owns multiple upstream FIFOs and emits bundles on each tick.
//
// The hub collects one ready item per upstream FIFO during each tick window.
// Items are ready when they are at the FIFO head or can be joined with the head.
// Non-joinable extra items remain queued for later ticks.
type mergeHub struct {
	// downstream receives bundle events.
	downstream *Stream

	// tickDuration controls the window size for collecting events.
	tickDuration time.Duration

	// perUpstreamBufferDepth controls the maximum FIFO depth per upstream.
	perUpstreamBufferDepth int

	// tickCounter increments on each flush to provide unique tick IDs.
	// Guarded by mu.
	tickCounter uint64

	// fifosByIdentity maps upstream identity to its FIFO.
	// Guarded by mu.
	fifosByIdentity map[upstreamIdentity]*upstreamFIFO

	mu sync.RWMutex

	// subscriptions maps upstream identity to subscription, used to detect closes.
	// Guarded by mu.
	subscriptions map[upstreamIdentity]*Subscription

	// ctx is the hub's context, canceled when hub stops.
	ctx context.Context

	// cancel stops the hub and all its goroutines.
	cancel context.CancelFunc

	// done closes when the hub has stopped and all goroutines have exited.
	done chan struct{}

	// errMu guards err.
	errMu sync.RWMutex

	// err holds the first non-context error encountered.
	err error
}

// newMergeHub creates a merge hub that will emit bundles to downstream.
// The hub runs in a background goroutine and collects upstream events into
// tick windows. tickDuration of zero disables hub mode.
func newMergeHub(ctx context.Context, downstream *Stream, tickDuration time.Duration, perUpstreamDepth int) *mergeHub {
	if perUpstreamDepth <= 0 {
		perUpstreamDepth = 1000
	}
	hubCtx, cancel := context.WithCancel(ctx)
	hub := &mergeHub{
		downstream:             downstream,
		tickDuration:           tickDuration,
		perUpstreamBufferDepth: perUpstreamDepth,
		fifosByIdentity:        make(map[upstreamIdentity]*upstreamFIFO),
		subscriptions:          make(map[upstreamIdentity]*Subscription),
		ctx:                    hubCtx,
		cancel:                 cancel,
		done:                   make(chan struct{}),
	}
	go hub.run()
	return hub
}

// addUpstream adds a new upstream FIFO to the hub.
// Returns error if the upstream is already registered.
func (h *mergeHub) addUpstream(ctx context.Context, upstream *Stream, identity upstreamIdentity, filter Filter, opts ...SubscribeOption) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, exists := h.fifosByIdentity[identity]; exists {
		return errors.New("upstream already registered")
	}

	sub, err := upstream.Subscribe(filter, opts...)
	if err != nil {
		return err
	}

	bufferDepth := 1000
	h.fifosByIdentity[identity] = newUpstreamFIFO(identity, bufferDepth)
	h.subscriptions[identity] = sub

	// Start goroutine to feed events from upstream into the FIFO.
	go h.feedUpstream(ctx, identity, sub)

	return nil
}

// feedUpstream reads events from a subscription and enqueues them into the FIFO.
// It stops when the subscription closes, the hub context is canceled, or an
// error occurs.
func (h *mergeHub) feedUpstream(ctx context.Context, identity upstreamIdentity, sub *Subscription) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.ctx.Done():
			return
		default:
		}

		event, ok, err := sub.Next(ctx)
		if err != nil {
			h.setErr(err)
			return
		}
		if !ok {
			// Subscription closed.
			h.mu.Lock()
			delete(h.fifosByIdentity, identity)
			delete(h.subscriptions, identity)
			h.mu.Unlock()
			return
		}

		// Enqueue event into the FIFO for this upstream.
		h.mu.Lock()
		fifo, ok := h.fifosByIdentity[identity]
		h.mu.Unlock()

		if !ok {
			// Upstream was removed while we were processing.
			return
		}

		// Try to join with the head event if the FIFO is not empty.
		// If join succeeds, the event is not added to the FIFO.
		if fifo.len() > 0 && fifo.tryJoinAtHead(event) {
			// Successfully joined - event was not added to FIFO.
			continue
		}

		// Join didn't happen, enqueue the event normally.
		if err := fifo.enqueue(event); err != nil {
			h.setErr(err)
			return
		}
	}
}

// run is the main hub loop that ticks and flushes bundle events.
func (h *mergeHub) run() {
	defer close(h.done)

	if h.tickDuration == 0 {
		// Hub mode disabled; never tick.
		<-h.ctx.Done()
		return
	}

	ticker := time.NewTicker(h.tickDuration)
	defer ticker.Stop()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			h.flushTick()
		}
	}
}

// flushTick collects one ready item per upstream and emits a bundle event.
// Ready items are at the FIFO head or have been joined at the head.
func (h *mergeHub) flushTick() {
	h.mu.Lock()
	if len(h.fifosByIdentity) == 0 {
		h.mu.Unlock()
		return
	}

	// Collect one item per FIFO.
	items := make([]Event, 0, len(h.fifosByIdentity))
	for _, fifo := range h.fifosByIdentity {
		event, ok := fifo.pop()
		if ok {
			items = append(items, event)
		}
	}

	// Increment tick counter for this flush.
	h.tickCounter++
	tickID := h.tickCounter
	h.mu.Unlock()

	if len(items) == 0 {
		return
	}

	// Create and encode bundle payload.
	bundle := BundlePayload{
		TickID:       tickID,
		NestedEvents: items,
	}

	delta, err := EncodeBundlePayload(bundle)
	if err != nil {
		h.setErr(err)
		return
	}

	// Emit bundle event.
	bundleEvent := Event{
		EventType: EventMergeBundle,
		From: Source{
			Layer: "hub",
		},
		Status: StatusCompleted,
		Delta:  delta,
	}

	if err := h.downstream.Emit(h.ctx, bundleEvent); err != nil {
		h.setErr(err)
	}
}

// Stop stops the hub and waits for it to exit.
func (h *mergeHub) Stop() {
	if h == nil {
		return
	}
	h.cancel()
	<-h.done
}

// Err reports the first non-context error encountered by the hub.
func (h *mergeHub) Err() error {
	h.errMu.RLock()
	defer h.errMu.RUnlock()
	return h.err
}

// setErr records the first non-context error.
func (h *mergeHub) setErr(err error) {
	if err == nil {
		return
	}
	h.errMu.Lock()
	defer h.errMu.Unlock()
	if h.err == nil && !errors.Is(err, context.Canceled) {
		h.err = err
	}
}
