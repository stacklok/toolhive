// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
