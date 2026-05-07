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
// by the downstream stream's shared merge hub. When TickDuration is 0 (default),
// events are forwarded directly.
type Merge struct {
	cancel context.CancelFunc
	done   chan struct{}

	errMu sync.RWMutex
	err   error

	// hub is non-nil when hub mode is enabled via MergeWindowConfig.
	hub *mergeHub
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
	return s.MergeWithConfig(ctx, upstream, filter, DefaultMergeWindowConfig(), opts...)
}

// MergeWithConfig forwards events from upstream into s with configurable
// hub behavior. When config.TickDuration > 0, events are collected into bundles
// by a downstream-owned merge hub shared with compatible hub-mode merges. When
// TickDuration is 0, uses direct forwarding (same as MergeFrom). Negative
// TickDuration values are rejected with [ErrInvalidMerge].
//
// filter and opts are applied while subscribing to upstream.
func (s *Stream) MergeWithConfig(ctx context.Context, upstream *Stream, filter Filter, config MergeWindowConfig, opts ...SubscribeOption) (*Merge, error) {
	if s == nil || upstream == nil {
		return nil, ErrInvalidMerge
	}
	if s == upstream {
		return nil, ErrInvalidMerge
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if config.TickDuration < 0 {
		return nil, ErrInvalidMerge
	}
	config = normalizeMergeWindowConfig(config)

	sub, err := upstream.Subscribe(filter, opts...)
	if err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	merge := &Merge{
		cancel: cancel,
		done:   make(chan struct{}),
	}

	if config.TickDuration == 0 {
		go merge.runDirect(runCtx, s, sub)
		return merge, nil
	}

	hub := s.mergeHub(config)
	hub.beginRegistration()
	merge.hub = hub
	go merge.runHubWithFirstEvent(runCtx, hub, sub)

	return merge, nil
}

// runHubWithFirstEvent reads the first event from the subscription to determine
// the upstream identity, registers it with the hub, and then feeds events.
func (m *Merge) runHubWithFirstEvent(ctx context.Context, hub *mergeHub, sub *Subscription) {
	readCtx, cancelRead := context.WithCancel(ctx)
	go func() {
		select {
		case <-readCtx.Done():
		case <-hub.ctx.Done():
			cancelRead()
		}
	}()
	defer func() {
		cancelRead()
		m.setErr(hub.Err())
		close(m.done)
	}()
	defer sub.Cancel()

	firstEvent, ok, err := sub.Next(readCtx)
	if err != nil {
		m.setErr(err)
		hub.cancelPendingRegistration()
		return
	}
	if !ok {
		hub.cancelPendingRegistration()
		return
	}

	identity := upstreamIdentityOf(firstEvent.From)
	if err := hub.registerPendingUpstream(identity, sub); err != nil {
		m.setErr(err)
		hub.stopIfIdle()
		return
	}
	if err := hub.enqueue(identity, firstEvent); err != nil {
		m.setErr(err)
		hub.unregisterUpstream(identity)
		return
	}

	for {
		select {
		case <-readCtx.Done():
			hub.unregisterUpstream(identity)
			return
		case <-hub.ctx.Done():
			return
		default:
		}

		event, ok, err := sub.Next(readCtx)
		if err != nil {
			m.setErr(err)
			hub.unregisterUpstream(identity)
			return
		}
		if !ok {
			hub.drainClosedUpstream(identity)
			return
		}

		if err := hub.enqueue(identity, event); err != nil {
			m.setErr(err)
			hub.unregisterUpstream(identity)
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

func prepareBundledMergedEvent(event Event, downstreamScope Scope) Event {
	event = prepareMergedEvent(event)
	event.Scope = mergeScope(downstreamScope, event.Scope)
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
	err := m.err
	m.errMu.RUnlock()
	if err != nil {
		return err
	}
	if m.hub != nil {
		return m.hub.Err()
	}
	return nil
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
// maxDepth must be positive; if not, [DefaultMergePerUpstreamBufferDepth] is used.
func newUpstreamFIFO(identity upstreamIdentity, maxDepth int) *upstreamFIFO {
	if maxDepth <= 0 {
		maxDepth = DefaultMergePerUpstreamBufferDepth
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
	f.events[0] = Event{}
	f.events = f.events[1:]
	return event, true
}

// popJoinedHead removes the FIFO head and joins any immediately adjacent
// same-key string deltas behind it into the returned event.
func (f *upstreamFIFO) popJoinedHead() (Event, bool) {
	event, ok := f.pop()
	if !ok {
		return Event{}, false
	}
	joinCount := 0
	innerLen := len(event.Delta) - 2
	for joinCount < len(f.events) {
		next := f.events[joinCount]
		if !canJoinEvents(event, next) {
			break
		}
		innerLen += len(next.Delta) - 2
		joinCount++
	}
	if joinCount == 0 {
		return event, true
	}
	joined := make([]byte, 0, innerLen+2)
	joined = append(joined, '"')
	joined = append(joined, event.Delta[1:len(event.Delta)-1]...)
	for i := 0; i < joinCount; i++ {
		next := f.events[i]
		joined = append(joined, next.Delta[1:len(next.Delta)-1]...)
		f.events[i] = Event{}
	}
	joined = append(joined, '"')
	event.Delta = joined
	f.events = f.events[joinCount:]
	return event, true
}

// len returns the number of events currently in the FIFO.
func (f *upstreamFIFO) len() int {
	return len(f.events)
}

// tryJoinAtHead attempts to join the second event with the first event at the
// FIFO head by combining their string deltas.
// If both events have the same join key and both have joinable string deltas,
// the deltas are combined (merged as JSON strings) into the first event.
// Returns true if join succeeded, false otherwise.
func (f *upstreamFIFO) tryJoinAtHead(nextEvent Event) bool {
	if len(f.events) < 1 {
		return false
	}
	return joinEventDelta(&f.events[0], nextEvent)
}

func joinEventDelta(base *Event, next Event) bool {
	if !canJoinEvents(*base, next) {
		return false
	}

	// Combine the deltas as JSON strings:
	// Remove quotes from both strings and merge the content.
	// "hello" + " world" => "hello world"
	prevStr := base.Delta[1 : len(base.Delta)-1]
	nextStr := next.Delta[1 : len(next.Delta)-1]
	combined := append([]byte(nil), append([]byte(`"`), append(prevStr, append(nextStr, '"')...)...)...)
	base.Delta = combined

	return true
}

func canJoinEvents(base Event, next Event) bool {
	return joinKeyOf(base) == joinKeyOf(next) && canJoinDeltas(base.Delta, next.Delta)
}

// MergeWindowConfig controls hub behavior for tick-based merging.
type MergeWindowConfig struct {
	// TickDuration is the time window for collecting events into a single bundle.
	// When zero, hub mode is disabled and direct forwarding is used.
	TickDuration time.Duration

	// PerUpstreamBufferDepth limits events queued per upstream before overflow.
	// When zero, [DefaultMergePerUpstreamBufferDepth] is used.
	PerUpstreamBufferDepth int
}

// DefaultMergeTickDuration is the standard tick window for production hub-mode
// stream merges.
const DefaultMergeTickDuration = 10 * time.Millisecond

// DefaultMergePerUpstreamBufferDepth is the standard per-upstream FIFO depth
// used by hub-mode stream merges.
const DefaultMergePerUpstreamBufferDepth = 4096

// DefaultMergeWindowConfig returns a MergeWindowConfig with direct-forwarding
// behavior (TickDuration = 0).
func DefaultMergeWindowConfig() MergeWindowConfig {
	return MergeWindowConfig{
		TickDuration:           0,
		PerUpstreamBufferDepth: DefaultMergePerUpstreamBufferDepth,
	}
}

// DefaultHubMergeWindowConfig returns a MergeWindowConfig with hub-mode
// bundling enabled using the standard production tick window.
func DefaultHubMergeWindowConfig() MergeWindowConfig {
	return MergeWindowConfig{
		TickDuration:           DefaultMergeTickDuration,
		PerUpstreamBufferDepth: DefaultMergePerUpstreamBufferDepth,
	}
}

type mergeHubKey struct {
	tickDuration           time.Duration
	perUpstreamBufferDepth int
}

func normalizeMergeWindowConfig(config MergeWindowConfig) MergeWindowConfig {
	if config.PerUpstreamBufferDepth <= 0 {
		config.PerUpstreamBufferDepth = DefaultMergePerUpstreamBufferDepth
	}
	return config
}

func mergeHubKeyOf(config MergeWindowConfig) mergeHubKey {
	config = normalizeMergeWindowConfig(config)
	return mergeHubKey{
		tickDuration:           config.TickDuration,
		perUpstreamBufferDepth: config.PerUpstreamBufferDepth,
	}
}

func (s *Stream) mergeHub(config MergeWindowConfig) *mergeHub {
	key := mergeHubKeyOf(config)

	s.mergeMu.Lock()
	defer s.mergeMu.Unlock()
	if hub := s.mergeHubs[key]; hub != nil && hub.active() {
		return hub
	}
	hub := newMergeHub(context.Background(), s, config.TickDuration, config.PerUpstreamBufferDepth)
	s.mergeHubs[key] = hub
	return hub
}

func (s *Stream) stopMergeHubs() {
	s.mergeMu.Lock()
	hubs := make([]*mergeHub, 0, len(s.mergeHubs))
	for _, hub := range s.mergeHubs {
		hubs = append(hubs, hub)
	}
	s.mergeHubs = make(map[mergeHubKey]*mergeHub)
	s.mergeMu.Unlock()

	for _, hub := range hubs {
		hub.setErr(ErrClosed)
		hub.Stop()
	}
}

// mergeHub owns multiple upstream FIFOs and emits bundles on each tick.
//
// The hub collects one ready item per upstream FIFO during each tick window.
// Items are ready when they are at the FIFO head or can be joined with adjacent
// same-key events behind the head.
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

	// pendingRegistrations counts merge feeders that have subscribed but have not
	// seen their first upstream event yet.
	// Guarded by mu.
	pendingRegistrations int

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
		perUpstreamDepth = DefaultMergePerUpstreamBufferDepth
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
	sub, err := upstream.Subscribe(filter, opts...)
	if err != nil {
		return err
	}
	if err := h.registerUpstream(identity, sub); err != nil {
		sub.Cancel()
		return err
	}

	// Start goroutine to feed events from upstream into the FIFO.
	go h.feedUpstream(ctx, identity, sub)

	return nil
}

func (h *mergeHub) registerUpstream(identity upstreamIdentity, sub *Subscription) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	select {
	case <-h.ctx.Done():
		return context.Canceled
	default:
	}
	if _, exists := h.fifosByIdentity[identity]; exists {
		return errors.New("upstream already registered")
	}
	h.fifosByIdentity[identity] = newUpstreamFIFO(identity, h.perUpstreamBufferDepth)
	h.subscriptions[identity] = sub
	return nil
}

func (h *mergeHub) beginRegistration() {
	h.mu.Lock()
	h.pendingRegistrations++
	h.mu.Unlock()
}

func (h *mergeHub) cancelPendingRegistration() {
	h.mu.Lock()
	if h.pendingRegistrations > 0 {
		h.pendingRegistrations--
	}
	stop := h.idleLocked()
	h.mu.Unlock()
	if stop {
		h.cancel()
	}
}

func (h *mergeHub) registerPendingUpstream(identity upstreamIdentity, sub *Subscription) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.pendingRegistrations > 0 {
		h.pendingRegistrations--
	}
	select {
	case <-h.ctx.Done():
		return context.Canceled
	default:
	}
	if _, exists := h.fifosByIdentity[identity]; exists {
		return errors.New("upstream already registered")
	}
	h.fifosByIdentity[identity] = newUpstreamFIFO(identity, h.perUpstreamBufferDepth)
	h.subscriptions[identity] = sub
	return nil
}

func (h *mergeHub) enqueue(identity upstreamIdentity, event Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	fifo, ok := h.fifosByIdentity[identity]
	if !ok {
		return ErrInvalidMerge
	}
	event = prepareBundledMergedEvent(event, h.downstream.scope)
	return fifo.enqueue(event)
}

func (h *mergeHub) closeUpstream(identity upstreamIdentity) {
	h.mu.Lock()
	delete(h.subscriptions, identity)
	if fifo, ok := h.fifosByIdentity[identity]; ok && fifo.len() == 0 {
		delete(h.fifosByIdentity, identity)
	}
	stop := h.idleLocked()
	h.mu.Unlock()
	if stop {
		h.cancel()
	}
}

func (h *mergeHub) unregisterUpstream(identity upstreamIdentity) {
	h.mu.Lock()
	delete(h.subscriptions, identity)
	delete(h.fifosByIdentity, identity)
	stop := h.idleLocked()
	h.mu.Unlock()
	if stop {
		h.cancel()
	}
}

func (h *mergeHub) drainClosedUpstream(identity upstreamIdentity) {
	h.closeUpstream(identity)
	for h.hasFIFO(identity) {
		if !h.flushTick() {
			break
		}
	}
	h.stopIfIdle()
}

func (h *mergeHub) hasFIFO(identity upstreamIdentity) bool {
	h.mu.RLock()
	_, ok := h.fifosByIdentity[identity]
	h.mu.RUnlock()
	return ok
}

func (h *mergeHub) stopIfIdle() {
	h.mu.Lock()
	stop := h.idleLocked()
	h.mu.Unlock()
	if stop {
		h.cancel()
	}
}

func (h *mergeHub) idleLocked() bool {
	return h.pendingRegistrations == 0 && len(h.subscriptions) == 0 && len(h.fifosByIdentity) == 0
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
			h.closeUpstream(identity)
			return
		}
		if !ok {
			h.closeUpstream(identity)
			return
		}
		if err := h.enqueue(identity, event); err != nil {
			h.setErr(err)
			h.closeUpstream(identity)
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
func (h *mergeHub) flushTick() bool {
	h.mu.Lock()
	if len(h.fifosByIdentity) == 0 {
		h.mu.Unlock()
		return false
	}

	// Collect one item per FIFO.
	items := make([]Event, 0, len(h.fifosByIdentity))
	for identity, fifo := range h.fifosByIdentity {
		event, ok := fifo.popJoinedHead()
		if ok {
			items = append(items, event)
		}
		if fifo.len() == 0 {
			if _, active := h.subscriptions[identity]; !active {
				delete(h.fifosByIdentity, identity)
			}
		}
	}

	stop := h.idleLocked()

	// Increment tick counter for this flush.
	h.tickCounter++
	tickID := h.tickCounter
	h.mu.Unlock()

	if len(items) == 0 {
		if stop {
			h.cancel()
		}
		return false
	}

	// Create and encode the event batch for this tick.
	bundle := EventBatch{
		TickID:       tickID,
		Events: items,
	}

	delta, err := EncodeEventBatch(bundle)
	if err != nil {
		h.setErr(err)
		if stop {
			h.cancel()
		}
		return false
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
		if stop {
			h.cancel()
		}
		return false
	}
	if stop {
		h.cancel()
	}
	return true
}

// Stop stops the hub and waits for it to exit.
func (h *mergeHub) Stop() {
	if h == nil {
		return
	}
	h.cancel()
	<-h.done
}

func (h *mergeHub) active() bool {
	select {
	case <-h.ctx.Done():
		return false
	default:
		return true
	}
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
