# Stream Merge Logical Time - PR 2 Memo

## Completed: PR 2 - Add process-local logical time

**PR**: https://github.com/TsumiNa/dango/pull/28

### Implementation Details

#### LogicalTime Field

```go
type Event struct {
	// ... existing fields ...
	LogicalTime    uint64          `json:"logical_time,omitempty"`
	// ... other fields ...
}
```

- Added to `Event` struct as `uint64`
- Uses `omitempty` tag for backward compatibility with existing JSON
- Zero value (0) used when unmarshaling old JSON without the field

#### Stream Logical Clock

```go
type Stream struct {
	// ... existing fields ...
	nextLogicalTime  uint64
	// ... other fields ...
}
```

- Each `Stream` instance maintains independent `nextLogicalTime` counter
- Starts at 0, increments with each `Emit()`
- Assigned **before** sequence number in critical section

#### Event.prepare() Signature Change

```go
func (ev Event) prepare(scope Scope, sequence uint64, logicalTime uint64, now func() time.Time) (Event, error)
```

- Now accepts `logicalTime uint64` parameter
- Called by `Stream.Emit()` which passes `s.nextLogicalTime + 1`
- Assigns both `sequence` and `logicalTime` to prepared event

#### Stream.Emit() Changes

```go
// In critical section:
sequence := s.nextSeq + 1
logicalTime := s.nextLogicalTime + 1
prepared, err := event.prepare(s.scope, sequence, logicalTime, s.now)
// ... error handling ...
s.nextSeq = sequence
s.nextLogicalTime = logicalTime
```

- Both counters incremented before storing
- LogicalTime assignment happens before sequence to maintain stable ordering

### Backward Compatibility

1. **JSON**: Old events without `logical_time` field unmarshal successfully with LogicalTime=0
2. **Replay**: Stored events can be replayed without requiring logical_time
3. **Merges**: Existing merge behavior unchanged; merged streams still receive events in FIFO order
4. **Filters**: Event filtering unaffected by new field

### Test Coverage

All tests in `internal/engine/stream/logical_time_test.go` pass:

1. **TestStreamEmitsIncreasingLogicalTimes**: 
   - Emits 5 events
   - Verifies logical times are 1, 2, 3, 4, 5
   - Confirms monotonic increment

2. **TestStreamLogicalTimeIndependentOfSequenceNumber**:
   - Verifies both sequence and logical time increase independently
   - Events have matching seq and logical_time (both 1, both 2)
   - Confirms they are not coupled

3. **TestLogicalTimeUnmarshalsTolerateMissingField**:
   - Old JSON without logical_time field unmarshals successfully
   - LogicalTime defaults to 0
   - All other fields preserved correctly

4. **TestLogicalTimeIncreasesAcrossStandaloneMerges**:
   - Creates two upstream streams and merges them
   - Verifies each stream has independent logical clock
   - Merged stream also has independent clock starting at 1

Updated tests:
- **TestEventPrepareJSONRoundTripIncludesRequiredFields**: Now verifies LogicalTime=42 preserved in roundtrip

### Per-Stream Clock Independence

```
Upstream Stream A:        Upstream Stream B:        Merged Stream:
Event seq=1, lt=1 ----\                          /---- Event seq=1, lt=1
Event seq=2, lt=2 ------> Merge Hub ----------->  Event seq=2, lt=2
Event seq=3, lt=3 ----/  B: seq=1, lt=1 \
                          B: seq=2, lt=2 /
```

Each stream (A, B, merged) maintains its own logical clock independent of others.

### JSON Schema Evolution

**New format** (with LogicalTime):
```json
{
  "event_type": "llm.output.delta",
  "from": {"layer": "skill"},
  "sequence_number": 5,
  "status": "running",
  "delta": {"text": "hello"},
  "logical_time": 5,
  "timestamp": "2026-05-07T10:30:00Z"
}
```

**Old format** (still supported, logical_time omitted):
```json
{
  "event_type": "llm.output.delta",
  "from": {"layer": "skill"},
  "sequence_number": 5,
  "status": "running",
  "delta": {"text": "hello"},
  "timestamp": "2026-05-07T10:30:00Z"
}
```

### Key Design Decisions

1. **omitempty on LogicalTime**: Keeps JSON compact; old consumers don't need to change
2. **Independent clocks per stream**: Allows distinguishing upstream timing from merged timing in later PRs
3. **Assigned in critical section**: Ensures stable ordering independent of goroutine scheduling
4. **Stamped before sequence**: Provides logical ordering that can't be misordered by concurrent access
5. **No validation**: LogicalTime zero value is valid; no check for "must be non-zero"

### Compatibility Notes

- Events replayed from storage maintain their original logical_time
- Merged streams observe upstream events with upstream logical_time values
- Consumers can ignore logical_time if they don't need stable ordering
- Zero values for old events don't break filtering or routing logic

## Next: PR 3 - Extract upstream identity and join-key helpers

Will add private helpers to identify upstream FIFO streams and determine which events can be joined in bundles by EventType and Status.
