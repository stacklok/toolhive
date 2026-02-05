// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

// HealthMonitor provides health status for backends (used for circuit breaker integration).
type HealthMonitor interface {
	// GetBackendState returns the health state for a backend.
	GetBackendState(backendID string) (*health.State, error)
	// IsBackendHealthy returns true if the backend is healthy.
	IsBackendHealthy(backendID string) bool
}

// defaultRouter is a stateless router implementation that retrieves routing
// information from the request context. With lazy discovery, capabilities are
// discovered per-request and stored in context by the discovery middleware.
//
// This router is thread-safe by design with immutable configuration fields.
type defaultRouter struct {
	partialFailureMode string        // "fail" or "best_effort" - controls routing behavior with circuit breaker
	healthMonitor      HealthMonitor // Optional health monitor for circuit breaker integration
}

// NewDefaultRouter creates a new default router instance.
// partialFailureMode controls behavior when circuit breaker is OPEN:
//   - "fail": Return error immediately if circuit breaker is OPEN
//   - "best_effort": Allow attempt even if circuit breaker is OPEN
// healthMonitor is optional (nil disables circuit breaker checking).
func NewDefaultRouter(partialFailureMode string, healthMonitor HealthMonitor) Router {
	return &defaultRouter{
		partialFailureMode: partialFailureMode,
		healthMonitor:      healthMonitor,
	}
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
// Checks circuit breaker state in fail mode before routing.
func (r *defaultRouter) RouteTool(ctx context.Context, toolName string) (*vmcp.BackendTarget, error) {
	target, err := routeCapability(
		ctx,
		toolName,
		func(rt *vmcp.RoutingTable) map[string]*vmcp.BackendTarget { return rt.Tools },
		"tools",
		"Tool",
		ErrToolNotFound,
	)
	if err != nil {
		return nil, err
	}

	// Check circuit breaker in fail mode
	if r.partialFailureMode == "fail" && r.healthMonitor != nil {
		state, err := r.healthMonitor.GetBackendState(target.WorkloadID)
		if err == nil && state.CircuitState == health.CircuitOpen {
			logger.Warnf("Circuit breaker OPEN for backend %s (tool: %s), failing in fail mode",
				target.WorkloadID, toolName)
			return nil, fmt.Errorf("%w: %s (circuit breaker open)", ErrBackendUnavailable, target.WorkloadID)
		}
	}

	return target, nil
}

// RouteResource resolves a resource URI to its backend target.
// With lazy discovery, this method gets capabilities from the request context
// instead of using a cached routing table.
// Checks circuit breaker state in fail mode before routing.
func (r *defaultRouter) RouteResource(ctx context.Context, uri string) (*vmcp.BackendTarget, error) {
	target, err := routeCapability(
		ctx,
		uri,
		func(rt *vmcp.RoutingTable) map[string]*vmcp.BackendTarget { return rt.Resources },
		"resources",
		"Resource",
		ErrResourceNotFound,
	)
	if err != nil {
		return nil, err
	}

	// Check circuit breaker in fail mode
	if r.partialFailureMode == "fail" && r.healthMonitor != nil {
		state, err := r.healthMonitor.GetBackendState(target.WorkloadID)
		if err == nil && state.CircuitState == health.CircuitOpen {
			logger.Warnf("Circuit breaker OPEN for backend %s (resource: %s), failing in fail mode",
				target.WorkloadID, uri)
			return nil, fmt.Errorf("%w: %s (circuit breaker open)", ErrBackendUnavailable, target.WorkloadID)
		}
	}

	return target, nil
}

// RoutePrompt resolves a prompt name to its backend target.
// With lazy discovery, this method gets capabilities from the request context
// instead of using a cached routing table.
// Checks circuit breaker state in fail mode before routing.
func (r *defaultRouter) RoutePrompt(ctx context.Context, name string) (*vmcp.BackendTarget, error) {
	target, err := routeCapability(
		ctx,
		name,
		func(rt *vmcp.RoutingTable) map[string]*vmcp.BackendTarget { return rt.Prompts },
		"prompts",
		"Prompt",
		ErrPromptNotFound,
	)
	if err != nil {
		return nil, err
	}

	// Check circuit breaker in fail mode
	if r.partialFailureMode == "fail" && r.healthMonitor != nil {
		state, err := r.healthMonitor.GetBackendState(target.WorkloadID)
		if err == nil && state.CircuitState == health.CircuitOpen {
			logger.Warnf("Circuit breaker OPEN for backend %s (prompt: %s), failing in fail mode",
				target.WorkloadID, name)
			return nil, fmt.Errorf("%w: %s (circuit breaker open)", ErrBackendUnavailable, target.WorkloadID)
		}
	}

	return target, nil
}
