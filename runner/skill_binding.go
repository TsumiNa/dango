package runner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tsumina/dango/llm"
	streampkg "github.com/tsumina/dango/stream"
)

type skillBinder interface {
	BindForRunner(sessID *string, runtimePaths AgentRuntimePaths, sessStores ...llm.SessionStore) (string, error)
}

type eventStreamProvider interface {
	EventStream() *streampkg.Stream
}

type memorySessionStore struct {
	mu       sync.Mutex
	sessions map[string][]llm.Event
}

func newMemorySessionStore() *memorySessionStore {
	return &memorySessionStore{sessions: make(map[string][]llm.Event)}
}

func (s *memorySessionStore) Append(ctx context.Context, sessionID string, ev *llm.Event) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if sessionID == "" {
		return 0, fmt.Errorf("runner: session id must not be empty")
	}
	if ev == nil {
		return 0, fmt.Errorf("runner: append requires a non-nil event")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	events := s.sessions[sessionID]
	switch {
	case len(events) == 0 && ev.Kind != llm.EventInit:
		return 0, llm.ErrSessionNotInitialised
	case len(events) > 0 && ev.Kind == llm.EventInit:
		return 0, llm.ErrSessionAlreadyInitialised
	}
	copyEvent := *ev
	copyEvent.Seq = int64(len(events) + 1)
	if copyEvent.Timestamp.IsZero() {
		copyEvent.Timestamp = time.Now()
	}
	s.sessions[sessionID] = append(events, copyEvent)
	return copyEvent.Seq, nil
}

func (s *memorySessionStore) Load(ctx context.Context, sessionID string) ([]llm.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	events, ok := s.sessions[sessionID]
	if !ok || len(events) == 0 {
		return nil, llm.ErrSessionNotFound
	}
	copyEvents := make([]llm.Event, len(events))
	copy(copyEvents, events)
	return copyEvents, nil
}

func (s *memorySessionStore) Truncate(ctx context.Context, sessionID string, toSeq int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	events, ok := s.sessions[sessionID]
	if !ok || len(events) == 0 {
		return llm.ErrSessionNotFound
	}
	if toSeq <= 0 {
		s.sessions[sessionID] = append([]llm.Event(nil), events[:1]...)
		return nil
	}
	if toSeq >= int64(len(events)) {
		return nil
	}
	s.sessions[sessionID] = append([]llm.Event(nil), events[:toSeq]...)
	return nil
}

func (s *memorySessionStore) Delete(ctx context.Context, sessionID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return llm.ErrSessionNotFound
	}
	delete(s.sessions, sessionID)
	return nil
}

func (r *Runner) prepareNodeAgents(nodes map[string]*Node) error {
	if len(nodes) == 0 || r.skillSessionStore == nil {
		return nil
	}
	for id, node := range nodes {
		if node == nil || node.Agent == nil {
			continue
		}
		runtimePaths, err := r.nodeRuntimePaths(id, node.SkillName, nil)
		if err != nil {
			return err
		}
		r.applySkillToolSet(node.SkillName, node.Agent)
		// Only bind the session here; do NOT merge the agent stream yet.
		// runNode calls prepareNodeAgent with the full runtime paths just
		// before execution, which re-binds and creates a fresh EventStream.
		// Merging here would subscribe to that first (throwaway) stream and
		// leak the goroutine when the second bind replaces it.
		if err := r.bindAgentSession(id, node.Agent, runtimePaths); err != nil {
			return err
		}
	}
	return nil
}

// bindAgentSession binds a persistent session for agent without touching
// the event stream. Stream merging is deferred to prepareNodeAgent, which
// is called with the correct accessibleDirs immediately before a node runs.
func (r *Runner) bindAgentSession(id string, agent Agent, runtimePaths AgentRuntimePaths) error {
	r.skillSessionMu.Lock()
	defer r.skillSessionMu.Unlock()

	binder, ok := agent.(skillBinder)
	if !ok {
		return nil
	}
	var sessionID *string
	if existing := r.skillSessionIDs[id]; existing != "" {
		existingCopy := existing
		sessionID = &existingCopy
	}
	boundSessionID, err := binder.BindForRunner(sessionID, runtimePaths, r.skillSessionStore)
	if err != nil {
		return fmt.Errorf("bind node %q session: %w", id, err)
	}
	if boundSessionID != "" {
		r.skillSessionIDs[id] = boundSessionID
	}
	return nil
}

func (r *Runner) prepareNodeAgent(id string, agent Agent, runtimePaths AgentRuntimePaths) error {
	if agent == nil || r.skillSessionStore == nil {
		return nil
	}
	r.applySkillToolSet(runtimePaths.SkillName, agent)
	r.skillSessionMu.Lock()
	defer r.skillSessionMu.Unlock()

	var sessionID *string
	if existing := r.skillSessionIDs[id]; existing != "" {
		existingCopy := existing
		sessionID = &existingCopy
	}

	binder, ok := agent.(skillBinder)
	if !ok {
		return r.mergeAgentStream(id, agent)
	}
	boundSessionID, err := binder.BindForRunner(sessionID, runtimePaths, r.skillSessionStore)
	if err != nil {
		return fmt.Errorf("prepare node %q agent: %w", id, err)
	}
	if boundSessionID != "" {
		r.skillSessionIDs[id] = boundSessionID
	}
	return r.mergeAgentStream(id, agent)
}

func (r *Runner) mergeAgentStream(id string, agent Agent) error {
	provider, ok := agent.(eventStreamProvider)
	if !ok {
		return nil
	}
	upstream := provider.EventStream()
	if upstream == nil {
		return nil
	}
	_, err := r.eventStream.MergeWithConfig(
		r.runtimeContext(context.Background()),
		upstream,
		streampkg.Filter{},
		streampkg.DefaultHubMergeWindowConfig(),
		streampkg.WithSubscriberBuffer(4096),
	)
	if err != nil {
		return fmt.Errorf("merge node %q stream: %w", id, err)
	}
	return nil
}
