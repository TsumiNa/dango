package stream

import "time"

// ChannelKind identifies one runner-managed markdown channel document kind.
type ChannelKind string

const (
	ChannelKindExchange ChannelKind = "exchange"
	ChannelKindHandoff  ChannelKind = "handoff"
	ChannelKindMemo     ChannelKind = "memo"
)

// ChannelHeader carries the shared metadata for runner-managed channel docs and
// compatible event payloads.
type ChannelHeader struct {
	Kind      ChannelKind `json:"kind,omitempty" yaml:"kind,omitempty"`
	Version   int         `json:"version,omitempty" yaml:"version,omitempty"`
	RunnerID  string      `json:"runner_id" yaml:"runner_id"`
	CreatedAt time.Time   `json:"created_at,omitempty" yaml:"created_at,omitempty"`
}
