package engine

import (
	"context"
	"encoding/json"
	"fmt"

	streampkg "github.com/tsumina/dango/internal/engine/stream"
)

func streamSourceOrchestrator() streampkg.Source {
	return streampkg.Source{Layer: "orchestrator", ID: "orchestrator"}
}

func emitEngineStreamEvent(ctx context.Context, eventStream *streampkg.Stream, source streampkg.Source, eventType string, status string, delta any, scope streampkg.Scope, metadata map[string]any) {
	if eventStream == nil {
		return
	}
	raw, err := json.Marshal(delta)
	if err != nil {
		raw, _ = json.Marshal(fmt.Sprint(delta))
	}
	_ = eventStream.Emit(ctx, streampkg.Event{
		EventType: eventType,
		From:      source,
		Status:    status,
		Delta:     json.RawMessage(raw),
		Scope:     scope,
		Metadata:  metadata,
	})
}
