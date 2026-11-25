// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import "context"

// ResourceMetadata carries trusted server-side facts about the resource being
// authorized that are not part of the public Authorizer method signature.
//
// BackendID is the logical vMCP backend identifier. It MUST be sourced from the
// aggregated capability, never from client-supplied request data such as tool
// arguments or an advertised-name prefix. Annotations carry the tool hints from
// that same trusted capability.
type ResourceMetadata struct {
	BackendID   string
	Annotations *ToolAnnotations
}

// resourceMetadataKey is the unexported context key used by
// WithResourceMetadata and ResourceMetadataFromContext.
type resourceMetadataKey struct{}

// WithResourceMetadata stores trusted resource metadata in ctx.
func WithResourceMetadata(ctx context.Context, metadata ResourceMetadata) context.Context {
	return context.WithValue(ctx, resourceMetadataKey{}, metadata)
}

// ResourceMetadataFromContext retrieves trusted resource metadata previously
// stored with WithResourceMetadata. For compatibility, annotations stored with
// WithToolAnnotations are returned when unified metadata has no annotations.
// The second return value reports whether either metadata channel is present.
func ResourceMetadataFromContext(ctx context.Context) (ResourceMetadata, bool) {
	metadata, ok := ctx.Value(resourceMetadataKey{}).(ResourceMetadata)
	if metadata.Annotations == nil {
		metadata.Annotations = effectiveToolAnnotationsFromContext(ctx)
		ok = ok || metadata.Annotations != nil
	}
	return metadata, ok
}
