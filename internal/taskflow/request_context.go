package taskflow

import "context"

type requestMetadataContextKey struct{}

// WithRequestMetadata stores normalized request entry metadata in a context.
//
// Orchestrator HTTP entrypoints use this helper so downstream request handling,
// task creation, and intent understanding can recover ingress metadata without
// threading RequestMetadata through every intermediate function signature.
func WithRequestMetadata(ctx context.Context, metadata RequestMetadata) context.Context {
	return context.WithValue(ctx, requestMetadataContextKey{}, metadata)
}

// RequestMetadataFromContext loads normalized request entry metadata from a
// context.
//
// When the context does not contain request metadata, the function returns the
// zero RequestMetadata value.
func RequestMetadataFromContext(ctx context.Context) RequestMetadata {
	if ctx == nil {
		return RequestMetadata{}
	}
	metadata, ok := ctx.Value(requestMetadataContextKey{}).(RequestMetadata)
	if !ok {
		return RequestMetadata{}
	}
	return metadata
}
