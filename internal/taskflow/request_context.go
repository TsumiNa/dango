package taskflow

import "context"

type requestMetadataContextKey struct{}

// WithRequestMetadata stores normalized request entry metadata in a context.
func WithRequestMetadata(ctx context.Context, metadata RequestMetadata) context.Context {
	return context.WithValue(ctx, requestMetadataContextKey{}, metadata)
}

// RequestMetadataFromContext loads normalized request entry metadata from a context.
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
