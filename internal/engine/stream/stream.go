package stream

import (
	"context"
	"sync"
	"time"
)

const (
	defaultBufferLimit      = 1 << 10
	defaultSubscriberBuffer = 16
)

// Config controls optional stream behavior.
type Config struct {
	// BufferLimit bounds the number of recent events kept in memory for replay.
	// Zero uses the default limit unless DisableBuffer is true. Negative values
	// are treated as zero.
	BufferLimit int

	// DisableBuffer disables the in-memory replay buffer.
	DisableBuffer bool
}

// DefaultConfig returns the default optional behavior for [New].
func DefaultConfig() Config {
	return Config{BufferLimit: defaultBufferLimit}
}

// Option adjusts a constructed [Stream] before it is returned.
type Option func(*Stream)

// WithStore installs store as the constructed Stream's durable event sink.
//
// The Stream keeps a reference to store and calls it from [Stream.Emit] while
// holding the stream's delivery lock. A nil store leaves durable replay
// disabled. If store is shared with other goroutines, callers are responsible
// for synchronization unless the Store implementation documents its own
// concurrency safety.
func WithStore(store Store) Option {
	return func(s *Stream) {
		s.store = store
	}
}

// Stream is an append-only, replayable channel-like communication bus for one
// logical execution scope.
//
// Producers publish structured events with [Stream.Emit]; consumers synchronize
// by subscribing with filters and reading from the returned subscription. Stream
// adds fan-out, replay, merge, scoped metadata, and optional persistence around
// the channel-shaped communication model used by orchestrator, runner, and
// skill goroutines. Executors and nodes add context around skill-owned streams
// and merge them upward rather than forming a separate execution substrate.
//
// Stream maintains a logical clock that provides stable event ordering
// independent of wall-clock time. Each emitted event receives a monotonically
// increasing LogicalTime before its per-stream sequence number.
type Stream struct {
	scope       Scope
	bufferLimit int
	store       Store
	now         func() time.Time

	mu              sync.Mutex
	deliveryMu      sync.Mutex
	mergeMu         sync.Mutex
	nextSeq         uint64
	nextLogicalTime uint64
	nextSubID       uint64
	closed          bool
	buffer          []Event
	subscribers     map[uint64]*Subscription
	mergeHubs       map[mergeHubKey]*mergeHub
}

// New creates a stream scoped to one request/run/session.
func New(scope Scope, cfg Config, opts ...Option) *Stream {
	bufferLimit := defaultBufferLimit
	if cfg.DisableBuffer {
		bufferLimit = 0
	} else if cfg.BufferLimit > 0 {
		bufferLimit = cfg.BufferLimit
	}
	s := &Stream{
		scope:       scope,
		bufferLimit: bufferLimit,
		now:         time.Now,
		subscribers: make(map[uint64]*Subscription),
		mergeHubs:   make(map[mergeHubKey]*mergeHub),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Emit appends and delivers one event. It assigns the stream sequence number,
// logical time, and fills default scope and timestamp fields before delivery.
func (s *Stream) Emit(ctx context.Context, event Event) error {
	if ctx == nil {
		ctx = context.Background()
	}

	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	sequence := s.nextSeq + 1
	logicalTime := s.nextLogicalTime + 1
	prepared, err := event.prepare(s.scope, sequence, logicalTime, s.now)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	matches := make([]*Subscription, 0, len(s.subscribers))
	for _, sub := range s.subscribers {
		if sub.filter.Match(prepared) {
			matches = append(matches, sub)
		}
	}
	if s.store != nil {
		if err := s.store.Append(ctx, prepared); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.nextSeq = sequence
	s.nextLogicalTime = logicalTime
	s.appendBufferLocked(prepared)
	s.mu.Unlock()

	for _, sub := range matches {
		if err := sub.send(ctx, prepared); err != nil {
			return err
		}
	}
	return nil
}

func (s *Stream) appendBufferLocked(event Event) {
	if s.bufferLimit == 0 {
		return
	}
	if len(s.buffer) == s.bufferLimit {
		copy(s.buffer, s.buffer[1:])
		s.buffer[len(s.buffer)-1] = event
		return
	}
	s.buffer = append(s.buffer, event)
}

// Close cancels all active subscriptions and rejects future emits or
// subscriptions.
func (s *Stream) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	subs := make([]*Subscription, 0, len(s.subscribers))
	for _, sub := range s.subscribers {
		subs = append(subs, sub)
	}
	s.mu.Unlock()

	for _, sub := range subs {
		sub.closeDone()
	}
	s.stopMergeHubs()

	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sub := range subs {
		delete(s.subscribers, sub.id)
		sub.closeEvents()
	}
}

// Subscribe attaches a consumer to the stream. Replay comes from the in-memory
// buffer when possible and falls back to the configured store when the
// requested range is older than the current buffer window. By default, live
// events that would block on a full subscriber channel are dropped for that
// subscriber; use [WithOverflowPolicy] to request an immediate overflow error
// instead.
func (s *Stream) Subscribe(filter Filter, opts ...SubscribeOption) (*Subscription, error) {
	settings := subscribeSettings{
		buffer:         defaultSubscriberBuffer,
		overflowPolicy: OverflowDropNewest,
	}
	for _, opt := range opts {
		opt(&settings)
	}
	if settings.buffer < 0 {
		settings.buffer = 0
	}

	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	id := s.nextSubID + 1
	replay, err := s.replayLocked(filter, settings)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	buffer := settings.buffer
	if buffer < len(replay) {
		buffer = len(replay)
	}
	sub := &Subscription{
		id:             id,
		stream:         s,
		filter:         filter,
		events:         make(chan Event, buffer),
		done:           make(chan struct{}),
		overflowPolicy: settings.overflowPolicy,
	}
	s.nextSubID = id
	s.subscribers[id] = sub
	s.mu.Unlock()

	for _, event := range replay {
		if err := sub.send(context.Background(), event); err != nil {
			sub.Cancel()
			return nil, err
		}
	}
	return sub, nil
}

// Replay returns a snapshot of buffered or stored events that match filter.
// It uses the same replay range options as [Stream.Subscribe] but does not
// attach a live subscriber. Merge bundle events are returned raw so debugging
// callers can inspect the persisted stream exactly as it was emitted.
func (s *Stream) Replay(filter Filter, opts ...SubscribeOption) ([]Event, error) {
	if s == nil {
		return nil, ErrClosed
	}
	settings := subscribeSettings{}
	for _, opt := range opts {
		opt(&settings)
	}

	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replayLocked(filter, settings)
}

// ReplayExpanded returns a replay snapshot with merge bundle events expanded
// into their nested events before filter is applied. Non-bundle events pass
// through the same filter. Use [Stream.Replay] when callers need the raw bundle
// events for stream debugging or persistence inspection.
func (s *Stream) ReplayExpanded(filter Filter, opts ...SubscribeOption) ([]Event, error) {
	raw, err := s.Replay(Filter{}, opts...)
	if err != nil {
		return nil, err
	}
	var replay []Event
	for _, event := range raw {
		events, err := FilterBundleEvent(event, filter)
		if err != nil {
			return nil, err
		}
		replay = append(replay, events...)
	}
	return replay, nil
}

func (s *Stream) replayLocked(filter Filter, settings subscribeSettings) ([]Event, error) {
	from := s.replayStartLocked(settings)
	if from == 0 {
		return nil, nil
	}
	oldestBuffered := s.oldestBufferedSequenceLocked()
	if s.store != nil && (oldestBuffered == 0 || from < oldestBuffered) {
		return s.store.Load(context.Background(), s.scope, from, filter)
	}
	var replay []Event
	for _, event := range s.buffer {
		if event.SequenceNumber < from {
			continue
		}
		if filter.Match(event) {
			replay = append(replay, event)
		}
	}
	return replay, nil
}

func (s *Stream) replayStartLocked(settings subscribeSettings) uint64 {
	if settings.noReplay {
		return 0
	}
	if settings.replayFrom == 0 && settings.replayLast == 0 {
		return s.defaultReplayStartLocked()
	}
	from := settings.replayFrom
	if settings.replayLast > 0 && s.nextSeq > 0 {
		last := uint64(settings.replayLast)
		lastFrom := uint64(1)
		if s.nextSeq >= last {
			lastFrom = s.nextSeq - last + 1
		}
		if lastFrom > from {
			from = lastFrom
		}
	}
	return from
}

func (s *Stream) defaultReplayStartLocked() uint64 {
	if oldest := s.oldestBufferedSequenceLocked(); oldest > 0 {
		return oldest
	}
	if s.bufferLimit == 0 || s.nextSeq == 0 {
		return 0
	}
	limit := uint64(s.bufferLimit)
	if s.nextSeq > limit {
		return s.nextSeq - limit + 1
	}
	return 1
}

func (s *Stream) oldestBufferedSequenceLocked() uint64 {
	if len(s.buffer) == 0 {
		return 0
	}
	return s.buffer[0].SequenceNumber
}
