// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"strings"

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
//
// Two naming conventions are supported:
//
//  1. Exact key: the resolved/conflict-resolved name stored in the routing
//     table (e.g. "my-backend_echo" after prefix conflict resolution).
//
//  2. Dot convention "{workloadID}.{toolName}": the tool name is the original
//     backend capability name and the workload ID is the prefix. This mirrors
//     the isToolStepAccessible logic used when registering composite tools and
//     lets workflow step definitions remain stable regardless of the conflict
//     resolution strategy in use.
//
// The dot convention is necessary because composite workflow steps reference
// tools by their pre-conflict-resolution name (e.g. "my-backend.echo"), while
// the routing table may store them under a prefixed key ("my-backend_echo").
func (r *sessionRouter) RouteTool(_ context.Context, toolName string) (*vmcp.BackendTarget, error) {
	if r.routingTable == nil || r.routingTable.Tools == nil {
		return nil, fmt.Errorf("%w: %s", ErrToolNotFound, toolName)
	}

	// Fast path: exact key match.
	if target, exists := r.routingTable.Tools[toolName]; exists {
		return target, nil
	}

	// Fallback: dot convention "{workloadID}.{toolName}".
	// Workload IDs are Kubernetes resource names and cannot contain dots,
	// so the first dot unambiguously separates the workload ID from the
	// original backend capability name.
	if dotIdx := strings.Index(toolName, "."); dotIdx > 0 {
		workloadID := toolName[:dotIdx]
		capName := toolName[dotIdx+1:]
		for resolvedName, target := range r.routingTable.Tools {
			if target.WorkloadID == workloadID && target.GetBackendCapabilityName(resolvedName) == capName {
				return target, nil
			}
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrToolNotFound, toolName)
}

// ResolveToolName returns the routing table key (conflict-resolved name) for
// toolName. If toolName is an exact key it is returned unchanged. If it uses
// the dot convention "{workloadID}.{originalCapabilityName}", the matching
// routing table key is returned. Falls back to returning toolName unchanged
// when the routing table is absent or the name cannot be resolved (pass-through
// semantics, consistent with the Router interface contract).
func (r *sessionRouter) ResolveToolName(_ context.Context, toolName string) string {
	if r.routingTable == nil || r.routingTable.Tools == nil {
		return toolName
	}

	// Fast path: exact key match.
	if _, exists := r.routingTable.Tools[toolName]; exists {
		return toolName
	}

	// Fallback: dot convention "{workloadID}.{toolName}".
	if dotIdx := strings.Index(toolName, "."); dotIdx > 0 {
		workloadID := toolName[:dotIdx]
		capName := toolName[dotIdx+1:]
		for resolvedName, target := range r.routingTable.Tools {
			if target.WorkloadID == workloadID && target.GetBackendCapabilityName(resolvedName) == capName {
				return resolvedName
			}
		}
	}

	return toolName
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
