// Package router provides request routing for Virtual MCP Server.
//
// This package routes MCP protocol requests (tools, resources, prompts) to
// appropriate backend workloads. It supports session affinity and pluggable
// routing strategies for load balancing.
package router

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Router routes MCP protocol requests to appropriate backend workloads.
// Implementations must be safe for concurrent use.
//
// The router maintains mappings from capability names (tools, resources, prompts)
// to backend targets, enabling the virtual MCP server to forward requests to the
// correct backend workload.
type Router interface {
	// RouteTool resolves a tool name to its backend target.
	// Returns ErrToolNotFound if the tool doesn't exist in any backend.
	RouteTool(ctx context.Context, toolName string) (*vmcp.BackendTarget, error)

	// RouteResource resolves a resource URI to its backend target.
	// Returns ErrResourceNotFound if the resource doesn't exist in any backend.
	RouteResource(ctx context.Context, uri string) (*vmcp.BackendTarget, error)

	// RoutePrompt resolves a prompt name to its backend target.
	// Returns ErrPromptNotFound if the prompt doesn't exist in any backend.
	RoutePrompt(ctx context.Context, name string) (*vmcp.BackendTarget, error)

	// UpdateRoutingTable updates the router's internal mappings.
	// Called after capability aggregation completes.
	UpdateRoutingTable(ctx context.Context, table *vmcp.RoutingTable) error
}

// RoutingStrategy defines how requests are routed when multiple backends
// can handle the same request (e.g., replicas for load balancing).
type RoutingStrategy interface {
	// SelectBackend chooses a backend from available candidates.
	// Returns ErrNoHealthyBackends if no backends are available.
	SelectBackend(ctx context.Context, candidates []*vmcp.BackendTarget) (*vmcp.BackendTarget, error)
}

// SessionAffinityProvider manages session-to-backend mappings.
// This ensures requests from the same MCP session are routed to the same backend.
type SessionAffinityProvider interface {
	// GetBackendForSession returns the backend for a given session ID.
	// Returns nil if no affinity exists for this session.
	GetBackendForSession(ctx context.Context, sessionID string) (*vmcp.BackendTarget, error)

	// SetBackendForSession establishes session affinity.
	SetBackendForSession(ctx context.Context, sessionID string, target *vmcp.BackendTarget) error

	// RemoveSession clears session affinity.
	RemoveSession(ctx context.Context, sessionID string) error
}

// Common routing errors.
var (
	// ErrToolNotFound indicates the requested tool doesn't exist.
	ErrToolNotFound = fmt.Errorf("tool not found")

	// ErrResourceNotFound indicates the requested resource doesn't exist.
	ErrResourceNotFound = fmt.Errorf("resource not found")

	// ErrPromptNotFound indicates the requested prompt doesn't exist.
	ErrPromptNotFound = fmt.Errorf("prompt not found")

	// ErrNoHealthyBackends indicates no healthy backends are available.
	ErrNoHealthyBackends = fmt.Errorf("no healthy backends available")

	// ErrBackendUnavailable indicates a backend is unavailable.
	ErrBackendUnavailable = fmt.Errorf("backend unavailable")
)
