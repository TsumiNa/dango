package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrRunnerLogNotFound is returned when the requested runner id has no log.
var ErrRunnerLogNotFound = errors.New("orchestrate: runner log not found")

// ErrRunnerLogNotInitialised is returned when a runner log is appended
// before it is anchored by a RunnerRecordInit record.
var ErrRunnerLogNotInitialised = errors.New("orchestrate: runner log must start with an init record")

// ErrRunnerLogAlreadyInitialised is returned when an init record is appended
// to a runner log that already has one.
var ErrRunnerLogAlreadyInitialised = errors.New("orchestrate: runner log already initialised")

// RunnerStore is an append-only persistence layer for Runner lifecycle data.
//
// Implementations must be safe for concurrent use. Records for a single
// runnerID are serialised so Seq values stay monotonic without gaps.
type RunnerStore interface {
	// Append writes rec to the log for runnerID, assigning rec.Seq from the
	// store's monotonic counter (starting at 1) and stamping rec.Timestamp when
	// it is zero.
	//
	// The first record written to a fresh runner log must have Kind
	// RunnerRecordInit; otherwise ErrRunnerLogNotInitialised is returned.
	Append(ctx context.Context, runnerID string, rec *RunnerRecord) (int64, error)

	// Load returns every fully written record for runnerID in Seq order.
	Load(ctx context.Context, runnerID string) ([]RunnerRecord, error)

	// Delete removes runnerID's log entirely.
	Delete(ctx context.Context, runnerID string) error
}

// RunnerRecordKind tags the kind of append-only runner record stored on disk.
type RunnerRecordKind string

const (
	RunnerRecordInit   RunnerRecordKind = "init"
	RunnerRecordStatus RunnerRecordKind = "status"
	RunnerRecordEvent  RunnerRecordKind = "event"
)

// RunnerRecord is one append-only record in a persisted runner log.
type RunnerRecord struct {
	Seq       int64            `json:"seq"`
	Kind      RunnerRecordKind `json:"kind"`
	Timestamp time.Time        `json:"ts"`

	Status RunnerStatus       `json:"status,omitempty"`
	Error  string             `json:"error,omitempty"`
	Event  *StoredRunnerEvent `json:"event,omitempty"`
}

// StoredRunnerEvent is the durable representation of a RunnerEvent.
//
// Data is stored either as raw JSON when the value is JSON-encodable or as
// plain text when the original payload was an error or could not be encoded.
type StoredRunnerEvent struct {
	Type         string          `json:"type"`
	NodeID       string          `json:"node_id,omitempty"`
	DataEncoding string          `json:"data_encoding,omitempty"`
	DataJSON     json.RawMessage `json:"data_json,omitempty"`
	DataText     string          `json:"data_text,omitempty"`
}

func newStoredRunnerEvent(event RunnerEvent) *StoredRunnerEvent {
	stored := &StoredRunnerEvent{Type: event.Type.String(), NodeID: event.NodeID}
	if event.Data == nil {
		return stored
	}
	if errValue, ok := event.Data.(error); ok {
		stored.DataEncoding = "text"
		stored.DataText = errValue.Error()
		return stored
	}
	if text, ok := event.Data.(string); ok && IsExchangeMarkdown(text) {
		stored.DataEncoding = "markdown"
		stored.DataText = text
		return stored
	}
	raw, err := json.Marshal(event.Data)
	if err != nil {
		stored.DataEncoding = "text"
		stored.DataText = fmt.Sprintf("%v", event.Data)
		return stored
	}
	stored.DataEncoding = "json"
	stored.DataJSON = raw
	return stored
}
