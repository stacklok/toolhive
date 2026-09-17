// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	// Results are always sorted alphabetically by backend name to ensure deterministic ordering.
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
//
//go:generate mockgen -destination=mocks/mock_interfaces.go -package=mocks -source=aggregator.go BackendDiscoverer Aggregator ConflictResolver ToolFilter ToolOverride
type Aggregator interface {
	// QueryCapabilities queries a backend for its MCP capabilities.
	// Returns the raw capabilities (tools, resources, prompts) from the backend.
	QueryCapabilities(ctx context.Context, backend vmcp.Backend) (*BackendCapabilities, error)

	// QueryAllCapabilities queries all backends for their capabilities in parallel.
	// How backend failures are treated follows the configured partial failure mode:
	// "best_effort" (or unset) logs and continues with remaining backends, while
	// "fail" makes the first failing backend fail the whole query.
	QueryAllCapabilities(ctx context.Context, backends []vmcp.Backend) (map[string]*BackendCapabilities, error)

	// ResolveConflicts applies conflict resolution strategy to handle
	// duplicate capability names across backends.
	ResolveConflicts(ctx context.Context, capabilities map[string]*BackendCapabilities) (*ResolvedCapabilities, error)

	// MergeCapabilities creates the final unified capability view and routing table.
	// Uses the backend registry to populate full BackendTarget information for routing.
	MergeCapabilities(
		ctx context.Context,
		resolved *ResolvedCapabilities,
		registry vmcp.BackendRegistry,
	) (*AggregatedCapabilities, error)

	// AggregateCapabilities is a convenience method that performs the full aggregation pipeline:
	// 1. Query all backends
	// 2. Resolve conflicts
	// 3. Merge into final view
	AggregateCapabilities(ctx context.Context, backends []vmcp.Backend) (*AggregatedCapabilities, error)
}

// BackendCapabilities contains the raw capabilities from a single backend.
type BackendCapabilities struct {
	// BackendID identifies the source backend.
	BackendID string

	// Tools are the tools exposed by this backend.
	Tools []vmcp.Tool

	// Resources are the resources exposed by this backend.
	Resources []vmcp.Resource

	// ResourceTemplates are the resource templates exposed by this backend.
	ResourceTemplates []vmcp.ResourceTemplate

	// Prompts are the prompts exposed by this backend.
	Prompts []vmcp.Prompt

	// SupportsLogging indicates if the backend supports MCP logging.
	SupportsLogging bool

	// SupportsSampling indicates if the backend supports MCP sampling.
	SupportsSampling bool
}

// ResolvedCapabilities contains capabilities after conflict resolution.
// Every capability identity (tool name, resource URI, resource template
// string, prompt name) is unique within its list.
type ResolvedCapabilities struct {
	// Tools are the conflict-resolved tools.
	// Map key is the resolved tool name, value contains original name and backend.
	Tools map[string]*ResolvedTool

	// Resources are de-duplicated by URI: a URI advertised by multiple backends
	// appears once, from the backend earliest in sorted-backend-ID order. URIs
	// are locators the client passes back verbatim, so they are never rewritten.
	// See resolveResourceConflicts.
	Resources []vmcp.Resource

	// ResourceTemplates are de-duplicated by URI template string, with the same
	// locator-identity policy as Resources. See resolveResourceTemplateConflicts.
	ResourceTemplates []vmcp.ResourceTemplate

	// Prompts are conflict-resolved by name: by default every prompt is
	// renamed to its backend-prefixed form; under the priority strategy,
	// backends listed in priorityOrder keep their bare names. Either way the
	// advertised name is a pure function of the aggregation config and
	// (backendID, name) — it never shifts with group membership. See
	// resolvePromptConflicts.
	Prompts []ResolvedPrompt

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

	// OutputSchema is the JSON Schema for tool output (optional).
	OutputSchema map[string]any

	// Annotations describes behavioral hints for the tool (optional).
	Annotations *vmcp.ToolAnnotations

	// BackendID identifies the backend providing this tool.
	BackendID string

	// ConflictResolutionApplied indicates which strategy was used.
	ConflictResolutionApplied vmcp.ConflictResolutionStrategy
}

// ResolvedPrompt represents a prompt after conflict resolution. The embedded
// Prompt is the advertised form: Name holds the resolved (client-visible)
// name. OriginalName is the name the backend itself uses; prompts/get and
// completion requests are translated back to it via
// BackendTarget.GetBackendCapabilityName, exactly like renamed tools.
type ResolvedPrompt struct {
	vmcp.Prompt

	// OriginalName is the prompt's name in the backend (equal to Name only
	// for priority-listed backends; otherwise Name is the backend-prefixed
	// form).
	OriginalName string
}

// AggregatedCapabilities is the final unified view of all backend capabilities.
// This is what gets exposed to MCP clients via tools/list, resources/list, prompts/list.
type AggregatedCapabilities struct {
	// Tools are the aggregated backend tools (ready to expose to clients),
	// sorted by name for deterministic ordering.
	Tools []vmcp.Tool

	// CompositeTools are the composite workflow tools defined in vMCP configuration.
	// These are separate from backend tools and orchestrate multi-step workflows.
	CompositeTools []vmcp.Tool

	// Resources are the aggregated resources.
	Resources []vmcp.Resource

	// ResourceTemplates are the aggregated resource templates.
	ResourceTemplates []vmcp.ResourceTemplate

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

	// ResourceTemplateCount is the total number of resource templates.
	ResourceTemplateCount int

	// PromptCount is the total number of prompts.
	PromptCount int

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

// CacheInvalidator is optionally implemented by an Aggregator that memoizes
// AggregateCapabilities results (see cachingAggregator). It lets a caller force
// a re-sweep of all cached entries after learning, out of band, that backend
// capabilities changed — e.g. a persistent backend connection observing
// notifications/tools/list_changed (#5748) — rather than waiting out the TTL.
//
// InvalidateAll purges the ENTIRE cache (every identity's entry), not just the
// backend that changed: the cache has no per-backend index, and coarse
// invalidation briefly de-optimizes other identities' cached views rather than
// leaving any identity looking at stale capabilities. Callers that type-assert
// an Aggregator to this interface must handle the case where it is not
// implemented (a non-caching or differently-implemented Aggregator) — see
// core.coreVMCP.InvalidateCapabilityCache for the WARN-log fallback.
type CacheInvalidator interface {
	// InvalidateAll purges every cached AggregateCapabilities entry so the next
	// call for any identity re-sweeps the backends.
	InvalidateAll()
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
