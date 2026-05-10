package stream

import "time"

// ChannelKind identifies one runner-managed markdown channel document kind.
type ChannelKind string

const (
	ChannelKindExchange ChannelKind = "exchange"
	ChannelKindHandoff  ChannelKind = "handoff"
	ChannelKindMemo     ChannelKind = "memo"

	LegacyChannelKindExchangeDoc ChannelKind = "dango.exchange_doc"
	LegacyChannelKindHandoffDoc  ChannelKind = "dango.handoff_doc"
	LegacyChannelKindMemoDoc     ChannelKind = "dango.memo"
)

// ChannelHeader carries the shared metadata for runner-managed channel docs and
// compatible event payloads.
type ChannelHeader struct {
	Kind      ChannelKind `json:"kind,omitempty" yaml:"kind,omitempty"`
	Version   int         `json:"version,omitempty" yaml:"version,omitempty"`
	RunnerID  string      `json:"runner_id" yaml:"runner_id"`
	CreatedAt time.Time   `json:"created_at,omitempty" yaml:"created_at,omitempty"`
}

// AcceptsChannelKind reports whether got matches want or one of want's legacy
// persisted aliases.
func AcceptsChannelKind(got ChannelKind, want ChannelKind) bool {
	for _, accepted := range acceptedChannelKinds(want) {
		if got == accepted {
			return true
		}
	}
	return false
}

func acceptedChannelKinds(kind ChannelKind) []ChannelKind {
	switch kind {
	case ChannelKindExchange:
		return []ChannelKind{ChannelKindExchange, LegacyChannelKindExchangeDoc}
	case ChannelKindHandoff:
		return []ChannelKind{ChannelKindHandoff, LegacyChannelKindHandoffDoc}
	case ChannelKindMemo:
		return []ChannelKind{ChannelKindMemo, LegacyChannelKindMemoDoc}
	default:
		return []ChannelKind{kind}
	}
}
