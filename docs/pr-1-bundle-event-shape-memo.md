# Stream Merge Bundle Event Shape - PR 1 Memo

## Completed: PR 1 - Add merge bundle event shape

**PR**: https://github.com/TsumiNa/dango/pull/27

### Implementation Details

#### Bundle Event Type

```go
const EventMergeBundle = "merge.bundle"
```

#### Bundle JSON Shape

```json
{
  "tick_id": 42,
  "nested_events": [
    {
      "event_type": "llm.output.delta",
      "from": {
        "layer": "skill",
        "id": "skill_1",
        "parent_id": "parent_id"
      },
      "sequence_number": 1,
      "status": "running",
      "delta": {"text": "hello"},
      "timestamp": "2026-05-07T10:30:00Z",
      "scope": {
        "request_id": "req_1",
        "runner_id": "run_1",
        "node_id": "node_1",
        "session_id": "sess_1"
      },
      "metadata": {"key": "value"}
    }
  ]
}
```

#### BundlePayload Structure

```go
type BundlePayload struct {
	TickID       uint64  `json:"tick_id"`
	NestedEvents []Event `json:"nested_events"`
}
```

- `TickID`: Logical tick identifier for this bundle window (used for ordering/debugging)
- `NestedEvents`: Slice of full `Event` structs emitted during this tick

#### Helper Functions

1. **EncodeBundlePayload(bundle BundlePayload) (json.RawMessage, error)**
   - Serializes a BundlePayload into JSON-encoded raw message
   - Suitable for use as Event.Delta field
   - Returns error if bundle cannot be marshaled

2. **DecodeBundlePayload(delta json.RawMessage) (BundlePayload, error)**
   - Deserializes a JSON-encoded bundle delta
   - Returns error if delta is not valid JSON or doesn't match expected shape

3. **IsValidBundlePayload(bundle BundlePayload) bool**
   - Validates that bundle has nested events (non-empty)
   - Returns false for empty bundles
   - Prevents emission of meaningless bundle events

### Test Coverage

All tests in `internal/engine/stream/bundle_test.go` pass:
- ✅ `TestEncodeBundlePayloadRoundTrip`: Nested event fields preserved
- ✅ `TestEmptyBundlePayloadIsInvalid`: Empty bundles rejected
- ✅ `TestBundlePayloadWithNestedFieldsPreservesMetadata`: Complex metadata preserved
- ✅ `TestDecodeBundlePayloadWithInvalidJSON`: Invalid input rejected
- ✅ `TestBundleEventTypeConstantExists`: Event type constant matches spec

All existing stream tests continue to pass (28 tests, 0.334s total).

### Key Design Decisions

1. **Bundle Contains Full Events**: Each nested event in the bundle is a complete `Event` struct with all fields (Source, Scope, Metadata, Timestamp, etc.) preserved through serialization.

2. **TickID Field**: Used by merge hub and consumers for:
   - Ordering bundle events from multiple ticks
   - Debugging merge behavior
   - Potential optimizations in later PRs

3. **Validation at Boundaries**: `IsValidBundlePayload()` enforces the contract that only non-empty bundles are emitted, making it explicit where validation happens.

4. **Minimal Helper API**: 
   - No merge logic or scheduling in this PR
   - No buffer management
   - Just encode/decode/validate
   - Functions colocated with bundle types in `bundle.go`

5. **No Behavior Changes**: This PR only defines data structures. Merge behavior (MergeFrom) remains unchanged; existing tests see no difference.

### Compatibility Notes

- Old streams without logical time will continue to work (LogicalTime field added in PR 2)
- Bundle events only used by new hub mode (direct-forwarding mode unchanged in PR 6)
- Existing JSON unmarshaling tolerates new fields due to go's json.Unmarshal behavior

## Next: PR 2 - Add process-local logical time

Will add `Event.LogicalTime uint64` field and stream-owned logical clock to ensure stable event ordering across bundles and replay.
