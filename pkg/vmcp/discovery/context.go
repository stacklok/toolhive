// Package discovery provides lazy per-user capability discovery for vMCP servers.
//
// This package handles context-based storage and retrieval of discovered backend
// capabilities within request-scoped contexts. The discovery process occurs
// asynchronously when a request arrives, and results are cached in the context
// to avoid redundant aggregation operations during request handling.
package discovery

import (
	"context"

	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

// contextKey is an unexported type for context keys to avoid collisions.
type contextKey struct{}

// discoveredCapabilitiesKey is the context key for storing aggregated capabilities.
var discoveredCapabilitiesKey = contextKey{}

// sessionIDKey is the context key for storing the session ID.
var sessionIDKey = contextKey{}

// WithDiscoveredCapabilities returns a new context with discovered capabilities attached.
// If capabilities is nil, the original context is returned unchanged.
func WithDiscoveredCapabilities(ctx context.Context, capabilities *aggregator.AggregatedCapabilities) context.Context {
	if capabilities == nil {
		return ctx
	}
	return context.WithValue(ctx, discoveredCapabilitiesKey, capabilities)
}

// DiscoveredCapabilitiesFromContext retrieves discovered capabilities from the context.
// Returns (nil, false) if capabilities are not found in the context.
func DiscoveredCapabilitiesFromContext(ctx context.Context) (*aggregator.AggregatedCapabilities, bool) {
	capabilities, ok := ctx.Value(discoveredCapabilitiesKey).(*aggregator.AggregatedCapabilities)
	return capabilities, ok
}

// WithSessionID returns a new context with the session ID attached.
// If sessionID is empty, the original context is returned unchanged.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// SessionIDFromContext retrieves the session ID from the context.
// Returns ("", false) if session ID is not found in the context.
func SessionIDFromContext(ctx context.Context) (string, bool) {
	sessionID, ok := ctx.Value(sessionIDKey).(string)
	return sessionID, ok
}
