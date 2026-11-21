// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import (
	"context"
)

// ToolCallImpliesRead decorates an Authorizer so that tool:call implies tool:read.
//
// A tool:read check is answered by the inner authorizer first. If that does not
// allow it, the decorator asks the inner authorizer the tool:call question. The
// implication is one-way: a tool:call check never falls back to tool:read, so a
// tool:read grant can never allow invocation.
//
// Discovery decisions that succeed only through the tool:call fallback set
// DiscoveryMarker.FallbackCount on the request's marker, so the audit trail can
// tell discovery apart from invocation even though the caller was allowed either way.
type ToolCallImpliesRead struct {
	inner Authorizer
}

// DiscoveryMarker is a mutable carrier that the audit middleware injects into the
// request context. The decorator increments FallbackCount when a tool:read decision was
// granted only by tool:call, and the audit middleware reads it back after the
// inner chain returns. It follows the same propagation pattern as
// mcp.AuthzDenialMarker, and it lives here because this package has no toolhive
// dependencies, so both pkg/audit and this decorator can import it.
//
// The marker is written and read by the single request goroutine, so no
// synchronization is needed.
type DiscoveryMarker struct {
	// FallbackCount is the number of tools granted only by the tool:call fallback
	// during this request.
	FallbackCount int
}

type discoveryMarkerContextKey struct{}

// WithDiscoveryMarker returns a new context carrying the given marker.
func WithDiscoveryMarker(ctx context.Context, marker *DiscoveryMarker) context.Context {
	return context.WithValue(ctx, discoveryMarkerContextKey{}, marker)
}

// DiscoveryMarkerFromContext retrieves the DiscoveryMarker from the context.
// Returns (nil, false) if no marker is present.
func DiscoveryMarkerFromContext(ctx context.Context) (*DiscoveryMarker, bool) {
	marker, ok := ctx.Value(discoveryMarkerContextKey{}).(*DiscoveryMarker)
	return marker, ok && marker != nil
}

// WithToolCallImpliesRead wraps a so that tool:call implies tool:read.
// A nil authorizer is returned unchanged, because nil means "no authorization".
func WithToolCallImpliesRead(a Authorizer) Authorizer {
	if a == nil {
		return nil
	}
	return &ToolCallImpliesRead{inner: a}
}

// ToolReadNative is implemented by an authorizer that decides tool:read itself, such as
// the HTTP PDP. AnswersToolRead returns true when the authorizer answers tool:read
// directly; false means it should be asked only tool:call, which is then the visibility
// decision. An authorizer that does not implement it (Cedar) gets read first, then call.
type ToolReadNative interface {
	AnswersToolRead() bool
}

// AuthorizeWithJWTClaims implements the Authorizer interface. Only tool:read
// requests go through the fallback; every other request is passed through as is.
func (d *ToolCallImpliesRead) AuthorizeWithJWTClaims(
	ctx context.Context,
	feature MCPFeature,
	operation MCPOperation,
	resourceID string,
	arguments map[string]interface{},
) (bool, error) {
	if feature != MCPFeatureTool || operation != MCPOperationRead {
		return d.inner.AuthorizeWithJWTClaims(ctx, feature, operation, resourceID, arguments)
	}

	if native, ok := d.inner.(ToolReadNative); ok {
		if native.AnswersToolRead() {
			// The authorizer answers tool:read itself, once. No call fallback follows a deny.
			return d.inner.AuthorizeWithJWTClaims(ctx, feature, MCPOperationRead, resourceID, arguments)
		}
		// Call-only: one tool:call check decides visibility, so the PDP sees no tool:read.
		callAllowed, callErr := d.inner.AuthorizeWithJWTClaims(ctx, feature, MCPOperationCall, resourceID, arguments)
		if callErr != nil || !callAllowed {
			return false, callErr
		}
		if marker, ok := DiscoveryMarkerFromContext(ctx); ok {
			marker.FallbackCount++
		}
		return true, nil
	}

	readAllowed, readErr := d.inner.AuthorizeWithJWTClaims(ctx, feature, MCPOperationRead, resourceID, arguments)
	if readErr != nil {
		// A tool:read error is returned as is. It says nothing about tool:call, and a
		// second PDP call would double the wait on a slow PDP.
		return false, readErr
	}
	if readAllowed {
		return true, nil
	}

	// tool:read was denied, so the tool:call answer decides.
	callAllowed, callErr := d.inner.AuthorizeWithJWTClaims(ctx, feature, MCPOperationCall, resourceID, arguments)
	if callErr != nil || !callAllowed {
		return false, callErr
	}
	if marker, ok := DiscoveryMarkerFromContext(ctx); ok {
		marker.FallbackCount++
	}
	return true, nil
}
