package stream

import (
	"context"
	"sync"
	"time"
)

const (
	defaultBufferLimit      = 256
	defaultSubscriberBuffer = 16
)

// Option customizes a Stream.
type Option func(*Stream)

// WithBufferLimit bounds the number of recent events kept for replay.
func WithBufferLimit(n int) Option {
	return func(s *Stream) {
		if n < 0 {
			n = 0
		}
		s.bufferLimit = n
	}
}

// WithStore attaches a durable event store. Emit appends to the store before
// subscribers receive the event.
func WithStore(store Store) Option {
	return func(s *Stream) {
		s.store = store
	}
}

// Stream is an append-only event bus for one logical execution scope.
type Stream struct {
	scope       Scope
	bufferLimit int
	store       Store
	now         func() time.Time

	mu          sync.Mutex
	deliveryMu  sync.Mutex
	nextSeq     uint64
	nextSubID   uint64
	closed      bool
	buffer      []Event
	subscribers map[uint64]*Subscription
}

// New creates a stream scoped to one request/run/session.
func New(scope Scope, opts ...Option) *Stream {
	s := &Stream{
		scope:       scope,
		bufferLimit: defaultBufferLimit,
		now:         time.Now,
		subscribers: make(map[uint64]*Subscription),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Emit appends and delivers one event. It assigns the stream sequence number
// and fills default scope and timestamp fields before delivery.
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
	prepared, err := event.prepare(s.scope, sequence, s.now)
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
