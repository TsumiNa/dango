package stream

import "strings"

// Filter selects the event subset a subscriber wants to receive.
//
// Empty filters match all events. When EventTypes or Prefixes are set, an event
// matches if either the exact type or one prefix matches.
type Filter struct {
	EventTypes []string
	Prefixes   []string
	Sources    []SourceSelector
	Statuses   []string
	Scope      Scope
}

// SourceSelector matches a producer. Empty fields are wildcards.
type SourceSelector struct {
	Layer    string
	ID       string
	ParentID string
}

// Match reports whether ev should be delivered to a subscriber.
func (f Filter) Match(ev Event) bool {
	if !f.matchEventType(ev.EventType) {
		return false
	}
	if !f.matchSource(ev.From) {
		return false
	}
	if !f.matchStatus(ev.Status) {
		return false
	}
	return f.matchScope(ev.Scope)
}

func (f Filter) matchEventType(eventType string) bool {
	if len(f.EventTypes) == 0 && len(f.Prefixes) == 0 {
		return true
	}
	for _, want := range f.EventTypes {
		if eventType == want {
			return true
		}
	}
	for _, prefix := range f.Prefixes {
		if strings.HasPrefix(eventType, prefix) {
			return true
		}
	}
	return false
}

func (f Filter) matchSource(source Source) bool {
	if len(f.Sources) == 0 {
		return true
	}
	for _, selector := range f.Sources {
		if selector.Layer != "" && selector.Layer != source.Layer {
			continue
		}
		if selector.ID != "" && selector.ID != source.ID {
			continue
		}
		if selector.ParentID != "" && selector.ParentID != source.ParentID {
			continue
		}
		return true
	}
	return false
}

func (f Filter) matchStatus(status string) bool {
	if len(f.Statuses) == 0 {
		return true
	}
	for _, want := range f.Statuses {
		if status == want {
			return true
		}
	}
	return false
}

func (f Filter) matchScope(scope Scope) bool {
	if f.Scope.RequestID != "" && f.Scope.RequestID != scope.RequestID {
		return false
	}
	if f.Scope.RunnerID != "" && f.Scope.RunnerID != scope.RunnerID {
		return false
	}
	if f.Scope.NodeID != "" && f.Scope.NodeID != scope.NodeID {
		return false
	}
	if f.Scope.SessionID != "" && f.Scope.SessionID != scope.SessionID {
		return false
	}
	return true
}
