# Stream Merge Bundle Consumption Helpers - PR 7 Memo

## Completed: PR 7 - Add bundle-aware downstream consumption helpers

**PR**: https://github.com/TsumiNa/dango/pull/33

### Implementation Details

#### Consumer Helper Names

```go
func ExpandBundleEvent(event Event) ([]Event, error)
func FilterBundleEvent(event Event, filter Filter) ([]Event, error)
```

#### ExpandBundleEvent

`ExpandBundleEvent` gives downstream consumers one path for both merge modes:

- `EventMergeBundle` events decode their `BundlePayload` and return nested events in bundle order.
- Expanded nested events inherit missing scope fields from the outer bundle event, matching direct merge scope-filter behavior.
- Non-bundle events return as a single-element slice unchanged.
- Malformed bundle payloads return an explicit error.
- Empty bundle payloads return an explicit error because they violate the bundle emission contract.

#### FilterBundleEvent

`FilterBundleEvent` calls `ExpandBundleEvent` and applies the existing `Filter.Match` logic to each resulting event. This keeps event type, prefix, source, status, and scope selection semantics identical between direct and bundled streams.

### Migration Pattern

Consumers that need to support both direct forwarding and hub mode can replace direct event handling with expansion first:

```go
expanded, err := stream.ExpandBundleEvent(event)
if err != nil {
	return err
}
for _, nested := range expanded {
	// handle nested exactly like a direct stream event
}
```

Consumers that already have a stream filter can use the filtered helper:

```go
expanded, err := stream.FilterBundleEvent(event, filter)
if err != nil {
	return err
}
for _, nested := range expanded {
	// handle matching nested events
}
```

### Test Coverage

Tests in `internal/engine/stream/bundle_test.go` cover:

- Bundle expansion preserves nested event order.
- Existing filters select matching nested events.
- Scope filters match nested events after outer bundle scope fields are merged in.
- Non-bundle events pass through unchanged.
- Malformed bundle payloads return an error.

### Compatibility Notes

- Direct-forwarding `MergeFrom` consumers can adopt the helpers before hub mode is enabled.
- Hub-mode consumers do not need to duplicate bundle JSON parsing.
- Filtering behavior remains owned by the existing `Filter` type.

## Next: PR 8 - Migrate one real caller to hub mode

Will enable hub mode in one narrow runtime merge path and update that path's consumer to expand bundle events.
