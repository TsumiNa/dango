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

// ExchangePublishedPayload is the JSON payload for [EventExchangePublished].
type ExchangePublishedPayload struct {
	ChannelHeader `json:",inline"`
	NodeID        string `json:"node_id"`
	Path          string `json:"path"`
	Document      string `json:"document"`
	Title         string `json:"title,omitempty"`
}

// HandoffArtifactPayload describes one artifact in handoff stream payloads.
type HandoffArtifactPayload struct {
	Path        string `json:"path"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// HandoffEmittedPayload is the JSON payload for [EventHandoffEmitted].
type HandoffEmittedPayload struct {
	ChannelHeader `json:",inline"`
	FromNode      string                   `json:"from_node"`
	ToNodes       []string                 `json:"to_nodes"`
	Intent        string                   `json:"intent,omitempty"`
	Path          string                   `json:"path"`
	Document      string                   `json:"document"`
	Artifacts     []HandoffArtifactPayload `json:"artifacts,omitempty"`
}

// HandoffDeliveredPayload is the JSON payload for [EventHandoffDelivered].
type HandoffDeliveredPayload struct {
	RunnerID      string                   `json:"runner_id"`
	FromNode      string                   `json:"from_node"`
	ToNode        string                   `json:"to_node"`
	InboxPath     string                   `json:"inbox_path"`
	HandoffPath   string                   `json:"handoff_path"`
	ArtifactPaths []string                 `json:"artifact_paths,omitempty"`
	Artifacts     []HandoffArtifactPayload `json:"artifacts,omitempty"`
	DeliveredAt   time.Time                `json:"delivered_at,omitempty"`
}

// MemoSnapshotPayload is the JSON payload for [EventMemoSnapshot].
type MemoSnapshotPayload struct {
	RunnerID    string    `json:"runner_id"`
	NodeID      string    `json:"node_id"`
	SkillName   string    `json:"skill_name,omitempty"`
	SnapshotDir string    `json:"snapshot_dir"`
	SnapshotAt  time.Time `json:"snapshot_at,omitempty"`
}
