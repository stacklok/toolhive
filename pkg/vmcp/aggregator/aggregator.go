// Package aggregator provides capability aggregation for Virtual MCP Server.
//
// This package discovers backend MCP servers, queries their capabilities,
// resolves naming conflicts, and merges them into a unified view.
// The aggregation process has three stages: query, conflict resolution, and merging.
package aggregator

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// BackendDiscoverer discovers backend MCP server workloads.
// This abstraction enables different discovery mechanisms for CLI (Docker/Podman)
// and Kubernetes (Pods/Services).
type BackendDiscoverer interface {
	// Discover finds all backend workloads in the specified group.
	// Returns only healthy/running backends.
	// The groupRef format is platform-specific (group name for CLI, MCPGroup name for K8s).
	Discover(ctx context.Context, groupRef string) ([]vmcp.Backend, error)
}

// Aggregator aggregates capabilities from discovered backends into a unified view.
// This is the core of the virtual MCP server's capability management.
//
// The aggregation process has three stages:
//  1. Query: Fetch capabilities from each backend
//  2. Conflict Resolution: Handle duplicate tool/resource/prompt names
//  3. Merging: Create final unified capability view and routing table
type Aggregator interface {
	// QueryCapabilities queries a backend for its MCP capabilities.
	// Returns the raw capabilities (tools, resources, prompts) from the backend.
	QueryCapabilities(ctx context.Context, backend vmcp.Backend) (*BackendCapabilities, error)

	// ResolveConflicts applies conflict resolution strategy to handle
	// duplicate capability names across backends.
	ResolveConflicts(ctx context.Context, capabilities map[string]*BackendCapabilities) (*ResolvedCapabilities, error)

	// MergeCapabilities creates the final unified capability view and routing table.
	MergeCapabilities(ctx context.Context, resolved *ResolvedCapabilities) (*AggregatedCapabilities, error)
}

// BackendCapabilities contains the raw capabilities from a single backend.
type BackendCapabilities struct {
	// BackendID identifies the source backend.
	BackendID string

	// Tools are the tools exposed by this backend.
	Tools []vmcp.Tool

	// Resources are the resources exposed by this backend.
	Resources []vmcp.Resource

	// Prompts are the prompts exposed by this backend.
	Prompts []vmcp.Prompt

	// SupportsLogging indicates if the backend supports MCP logging.
	SupportsLogging bool

	// SupportsSampling indicates if the backend supports MCP sampling.
	SupportsSampling bool
}

// ResolvedCapabilities contains capabilities after conflict resolution.
// Tool names are now unique (after prefixing, priority, or manual resolution).
type ResolvedCapabilities struct {
	// Tools are the conflict-resolved tools.
	// Map key is the resolved tool name, value contains original name and backend.
	Tools map[string]*ResolvedTool

	// Resources are passed through (conflicts rare, namespaced by URI).
	Resources []vmcp.Resource

	// Prompts are passed through (conflicts rare, namespaced by name).
	Prompts []vmcp.Prompt

	// SupportsLogging is true if any backend supports logging.
	SupportsLogging bool

	// SupportsSampling is true if any backend supports sampling.
	SupportsSampling bool
}

// ResolvedTool represents a tool after conflict resolution.
type ResolvedTool struct {
	// ResolvedName is the final name exposed to clients (after conflict resolution).
	ResolvedName string

	// OriginalName is the tool's name in the backend.
	OriginalName string

	// Description is the tool description (may be overridden).
	Description string

	// InputSchema is the JSON Schema for parameters.
	InputSchema map[string]any

	// BackendID identifies the backend providing this tool.
	BackendID string

	// ConflictResolutionApplied indicates which strategy was used.
	ConflictResolutionApplied vmcp.ConflictResolutionStrategy
}

// AggregatedCapabilities is the final unified view of all backend capabilities.
// This is what gets exposed to MCP clients via tools/list, resources/list, prompts/list.
type AggregatedCapabilities struct {
	// Tools are the aggregated tools (ready to expose to clients).
	Tools []vmcp.Tool

	// Resources are the aggregated resources.
	Resources []vmcp.Resource

	// Prompts are the aggregated prompts.
	Prompts []vmcp.Prompt

	// SupportsLogging indicates if logging is supported.
	SupportsLogging bool

	// SupportsSampling indicates if sampling is supported.
	SupportsSampling bool

	// RoutingTable maps capabilities to their backend targets.
	RoutingTable *vmcp.RoutingTable

	// Metadata contains aggregation statistics and info.
	Metadata *AggregationMetadata
}

// AggregationMetadata contains information about the aggregation process.
type AggregationMetadata struct {
	// BackendCount is the number of backends aggregated.
	BackendCount int

	// ToolCount is the total number of tools.
	ToolCount int

	// ResourceCount is the total number of resources.
	ResourceCount int

	// PromptCount is the total number of prompts.
	PromptCount int

	// ConflictsResolved is the number of conflicts that were resolved.
	ConflictsResolved int

	// ConflictStrategy is the strategy used for conflict resolution.
	ConflictStrategy vmcp.ConflictResolutionStrategy
}

// ConflictResolver handles tool name conflicts across backends.
type ConflictResolver interface {
	// ResolveToolConflicts resolves tool name conflicts using the configured strategy.
	ResolveToolConflicts(ctx context.Context, tools map[string][]vmcp.Tool) (map[string]*ResolvedTool, error)
}

// ToolFilter filters tools from a backend based on configuration.
// This reuses ToolHive's existing mcp.WithToolsFilter() middleware.
type ToolFilter interface {
	// FilterTools returns only the tools that should be included.
	FilterTools(ctx context.Context, tools []vmcp.Tool) ([]vmcp.Tool, error)
}

// ToolOverride applies renames and description updates to tools.
// This reuses ToolHive's existing mcp.WithToolsOverride() middleware.
type ToolOverride interface {
	// ApplyOverrides modifies tool names and descriptions.
	ApplyOverrides(ctx context.Context, tools []vmcp.Tool) ([]vmcp.Tool, error)
}

// Common aggregation errors.
var (
	// ErrNoBackendsFound indicates no backends were discovered.
	ErrNoBackendsFound = fmt.Errorf("no backends found in group")

	// ErrBackendQueryFailed indicates a backend query failed.
	ErrBackendQueryFailed = fmt.Errorf("failed to query backend capabilities")

	// ErrUnresolvedConflicts indicates conflicts exist without resolution.
	ErrUnresolvedConflicts = fmt.Errorf("unresolved capability name conflicts")

	// ErrInvalidConflictStrategy indicates an unknown conflict resolution strategy.
	ErrInvalidConflictStrategy = fmt.Errorf("invalid conflict resolution strategy")
)
