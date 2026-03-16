// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// sessionRouter is a Router implementation backed directly by a RoutingTable,
// requiring no request context to resolve capabilities. It is used by
// per-session workflow engines so that composite tool execution does not depend
// on the discovery middleware injecting DiscoveredCapabilities into the context.
type sessionRouter struct {
	routingTable *vmcp.RoutingTable
}

// NewSessionRouter creates a Router that routes from the provided RoutingTable
// without reading the request context. This is the preferred router for
// composite tool workflow engines because it couples routing to the session
// rather than to middleware-managed context values.
func NewSessionRouter(rt *vmcp.RoutingTable) Router {
	return &sessionRouter{routingTable: rt}
}

// RouteTool resolves a tool name to its backend target using the session's
// routing table directly.
func (r *sessionRouter) RouteTool(_ context.Context, toolName string) (*vmcp.BackendTarget, error) {
	if r.routingTable == nil || r.routingTable.Tools == nil {
		return nil, fmt.Errorf("%w: %s", ErrToolNotFound, toolName)
	}
	target, exists := r.routingTable.Tools[toolName]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrToolNotFound, toolName)
	}
	return target, nil
}

// RouteResource resolves a resource URI to its backend target using the
// session's routing table directly.
func (r *sessionRouter) RouteResource(_ context.Context, uri string) (*vmcp.BackendTarget, error) {
	if r.routingTable == nil || r.routingTable.Resources == nil {
		return nil, fmt.Errorf("%w: %s", ErrResourceNotFound, uri)
	}
	target, exists := r.routingTable.Resources[uri]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrResourceNotFound, uri)
	}
	return target, nil
}

// RoutePrompt resolves a prompt name to its backend target using the session's
// routing table directly.
func (r *sessionRouter) RoutePrompt(_ context.Context, name string) (*vmcp.BackendTarget, error) {
	if r.routingTable == nil || r.routingTable.Prompts == nil {
		return nil, fmt.Errorf("%w: %s", ErrPromptNotFound, name)
	}
	target, exists := r.routingTable.Prompts[name]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrPromptNotFound, name)
	}
	return target, nil
}
