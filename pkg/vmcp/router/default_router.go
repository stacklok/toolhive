// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
)

// defaultRouter is a stateless router implementation that retrieves routing
// information from the request context. With lazy discovery, capabilities are
// discovered per-request and stored in context by the discovery middleware.
//
// This router is thread-safe by design since it maintains no mutable state.
type defaultRouter struct {
	// No fields - routing table comes from request context
}

// NewDefaultRouter creates a new default router instance.
func NewDefaultRouter() Router {
	return &defaultRouter{}
}

// routeCapability is a generic helper that implements the common routing logic
// for tools, resources, and prompts. It extracts capabilities from context,
// validates the routing table, and looks up the key in the specified map.
//
// Parameters:
//   - ctx: The request context containing discovered capabilities
//   - key: The capability identifier (tool name, resource URI, or prompt name)
//   - getMap: Function to extract the specific map from the routing table
//   - mapName: Name of the map for error messages (e.g., "tools", "resources", "prompts")
//   - entityType: Type of entity for log messages (e.g., "tool", "resource", "prompt")
//   - notFoundErr: The specific error to wrap when the key is not found
func routeCapability(
	ctx context.Context,
	key string,
	getMap func(*vmcp.RoutingTable) map[string]*vmcp.BackendTarget,
	mapName string,
	entityType string,
	notFoundErr error,
) (*vmcp.BackendTarget, error) {
	// Defensive nil check - prevent panic if context is nil
	if ctx == nil {
		return nil, fmt.Errorf("context cannot be nil")
	}

	// Get capabilities from context (set by discovery middleware)
	capabilities, ok := discovery.DiscoveredCapabilitiesFromContext(ctx)
	if !ok || capabilities == nil {
		return nil, fmt.Errorf("capabilities not found in context - discovery middleware may not have run")
	}

	if capabilities.RoutingTable == nil {
		return nil, fmt.Errorf("routing table not initialized in discovered capabilities")
	}

	capabilityMap := getMap(capabilities.RoutingTable)
	if capabilityMap == nil {
		return nil, fmt.Errorf("routing table %s map not initialized", mapName)
	}

	target, exists := capabilityMap[key]
	if !exists {
		logger.Debugf("%s not found in routing table: %s", entityType, key)
		return nil, fmt.Errorf("%w: %s", notFoundErr, key)
	}

	logger.Debugf("Routed %s %s to backend %s", entityType, key, target.WorkloadID)
	return target, nil
}

// RouteTool resolves a tool name to its backend target.
// With lazy discovery, this method gets capabilities from the request context
// instead of using a cached routing table.
func (*defaultRouter) RouteTool(ctx context.Context, toolName string) (*vmcp.BackendTarget, error) {
	return routeCapability(
		ctx,
		toolName,
		func(rt *vmcp.RoutingTable) map[string]*vmcp.BackendTarget { return rt.Tools },
		"tools",
		"Tool",
		ErrToolNotFound,
	)
}

// RouteResource resolves a resource URI to its backend target.
// With lazy discovery, this method gets capabilities from the request context
// instead of using a cached routing table.
func (*defaultRouter) RouteResource(ctx context.Context, uri string) (*vmcp.BackendTarget, error) {
	return routeCapability(
		ctx,
		uri,
		func(rt *vmcp.RoutingTable) map[string]*vmcp.BackendTarget { return rt.Resources },
		"resources",
		"Resource",
		ErrResourceNotFound,
	)
}

// RoutePrompt resolves a prompt name to its backend target.
// With lazy discovery, this method gets capabilities from the request context
// instead of using a cached routing table.
func (*defaultRouter) RoutePrompt(ctx context.Context, name string) (*vmcp.BackendTarget, error) {
	return routeCapability(
		ctx,
		name,
		func(rt *vmcp.RoutingTable) map[string]*vmcp.BackendTarget { return rt.Prompts },
		"prompts",
		"Prompt",
		ErrPromptNotFound,
	)
}
