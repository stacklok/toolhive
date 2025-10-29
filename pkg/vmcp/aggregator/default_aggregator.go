package aggregator

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// defaultAggregator implements the Aggregator interface for capability aggregation.
// It queries backends in parallel, handles failures gracefully, and merges capabilities.
type defaultAggregator struct {
	backendClient vmcp.BackendClient
	// TODO: Add conflict resolver, tool filter, tool override
}

// NewDefaultAggregator creates a new default aggregator implementation.
func NewDefaultAggregator(backendClient vmcp.BackendClient) Aggregator {
	return &defaultAggregator{
		backendClient: backendClient,
	}
}

// QueryCapabilities queries a single backend for its MCP capabilities.
// Returns the raw capabilities (tools, resources, prompts) from the backend.
func (a *defaultAggregator) QueryCapabilities(ctx context.Context, backend vmcp.Backend) (*BackendCapabilities, error) {
	logger.Debugf("Querying capabilities from backend %s", backend.ID)

	// Create a BackendTarget from the Backend
	target := &vmcp.BackendTarget{
		WorkloadID:    backend.ID,
		WorkloadName:  backend.Name,
		BaseURL:       backend.BaseURL,
		TransportType: backend.TransportType,
		HealthStatus:  backend.HealthStatus,
		Metadata:      backend.Metadata,
	}

	// Query capabilities using the backend client
	capabilities, err := a.backendClient.ListCapabilities(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrBackendQueryFailed, backend.ID, err)
	}

	// Convert to BackendCapabilities
	result := &BackendCapabilities{
		BackendID:        backend.ID,
		Tools:            capabilities.Tools,
		Resources:        capabilities.Resources,
		Prompts:          capabilities.Prompts,
		SupportsLogging:  capabilities.SupportsLogging,
		SupportsSampling: capabilities.SupportsSampling,
	}

	logger.Debugf("Backend %s: %d tools, %d resources, %d prompts",
		backend.ID, len(result.Tools), len(result.Resources), len(result.Prompts))

	return result, nil
}

// QueryAllCapabilities queries all backends for their capabilities in parallel.
// Handles backend failures gracefully (logs and continues with remaining backends).
func (a *defaultAggregator) QueryAllCapabilities(
	ctx context.Context,
	backends []vmcp.Backend,
) (map[string]*BackendCapabilities, error) {
	logger.Infof("Querying capabilities from %d backends", len(backends))

	// Use errgroup for parallel queries with context cancellation
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(10) // Limit concurrent queries to avoid overwhelming backends

	// Thread-safe map for results
	var mu sync.Mutex
	capabilities := make(map[string]*BackendCapabilities)

	// Query each backend in parallel
	for _, backend := range backends {
		backend := backend // Capture loop variable
		g.Go(func() error {
			caps, err := a.QueryCapabilities(ctx, backend)
			if err != nil {
				// Log the error but continue with other backends
				logger.Warnf("Failed to query backend %s: %v", backend.ID, err)
				return nil // Don't fail the entire operation
			}

			// Store result safely
			mu.Lock()
			capabilities[backend.ID] = caps
			mu.Unlock()

			return nil
		})
	}

	// Wait for all queries to complete
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("capability queries failed: %w", err)
	}

	if len(capabilities) == 0 {
		return nil, fmt.Errorf("no backends returned capabilities")
	}

	logger.Infof("Successfully queried %d/%d backends", len(capabilities), len(backends))
	return capabilities, nil
}

// ResolveConflicts applies conflict resolution strategy to handle
// duplicate capability names across backends.
func (*defaultAggregator) ResolveConflicts(
	_ context.Context,
	capabilities map[string]*BackendCapabilities,
) (*ResolvedCapabilities, error) {
	logger.Debugf("Resolving conflicts across %d backends", len(capabilities))

	// For Phase 1 (Issue #148), we'll implement basic conflict resolution
	// Just collect all capabilities without resolving conflicts yet
	// Conflict resolution will be implemented in a future phase

	resolved := &ResolvedCapabilities{
		Tools:     make(map[string]*ResolvedTool),
		Resources: []vmcp.Resource{},
		Prompts:   []vmcp.Prompt{},
	}

	// Collect all tools (for now, without conflict resolution)
	// Later, we'll add prefix/priority/manual strategies
	for backendID, caps := range capabilities {
		for _, tool := range caps.Tools {
			// For now, just use the tool name as-is
			// In future phases, we'll apply prefixing or priority rules
			resolvedName := tool.Name

			// If there's a conflict, log a warning (but don't fail)
			if existing, exists := resolved.Tools[resolvedName]; exists {
				logger.Warnf("Tool name conflict: %s exists in both %s and %s (keeping first)",
					resolvedName, existing.BackendID, backendID)
				continue
			}

			resolved.Tools[resolvedName] = &ResolvedTool{
				ResolvedName: resolvedName,
				OriginalName: tool.Name,
				Description:  tool.Description,
				InputSchema:  tool.InputSchema,
				BackendID:    tool.BackendID,
				// ConflictResolutionApplied will be set in future phases
			}
		}

		// Collect resources (URIs should be globally unique)
		resolved.Resources = append(resolved.Resources, caps.Resources...)

		// Collect prompts
		resolved.Prompts = append(resolved.Prompts, caps.Prompts...)

		// Aggregate logging/sampling support (OR logic - enabled if any backend supports)
		resolved.SupportsLogging = resolved.SupportsLogging || caps.SupportsLogging
		resolved.SupportsSampling = resolved.SupportsSampling || caps.SupportsSampling
	}

	logger.Debugf("Resolved %d unique tools, %d resources, %d prompts",
		len(resolved.Tools), len(resolved.Resources), len(resolved.Prompts))

	return resolved, nil
}

// MergeCapabilities creates the final unified capability view and routing table.
// Uses the backend registry to populate full BackendTarget information for routing.
func (*defaultAggregator) MergeCapabilities(
	ctx context.Context,
	resolved *ResolvedCapabilities,
	registry vmcp.BackendRegistry,
) (*AggregatedCapabilities, error) {
	logger.Debugf("Merging capabilities into final view")

	// Create routing table
	routingTable := &vmcp.RoutingTable{
		Tools:     make(map[string]*vmcp.BackendTarget),
		Resources: make(map[string]*vmcp.BackendTarget),
		Prompts:   make(map[string]*vmcp.BackendTarget),
	}

	// Convert resolved tools to final vmcp.Tool format
	tools := make([]vmcp.Tool, 0, len(resolved.Tools))
	for _, resolvedTool := range resolved.Tools {
		tools = append(tools, vmcp.Tool{
			Name:        resolvedTool.ResolvedName,
			Description: resolvedTool.Description,
			InputSchema: resolvedTool.InputSchema,
			BackendID:   resolvedTool.BackendID,
		})

		// Look up full backend information from registry
		backend := registry.Get(ctx, resolvedTool.BackendID)
		if backend == nil {
			logger.Warnf("Backend %s not found in registry for tool %s, creating minimal target",
				resolvedTool.BackendID, resolvedTool.ResolvedName)
			routingTable.Tools[resolvedTool.ResolvedName] = &vmcp.BackendTarget{
				WorkloadID: resolvedTool.BackendID,
			}
		} else {
			// Use the backendToTarget helper from registry package
			routingTable.Tools[resolvedTool.ResolvedName] = vmcp.BackendToTarget(backend)
		}
	}

	// Add resources to routing table
	for _, resource := range resolved.Resources {
		backend := registry.Get(ctx, resource.BackendID)
		if backend == nil {
			logger.Warnf("Backend %s not found in registry for resource %s, creating minimal target",
				resource.BackendID, resource.URI)
			routingTable.Resources[resource.URI] = &vmcp.BackendTarget{
				WorkloadID: resource.BackendID,
			}
		} else {
			routingTable.Resources[resource.URI] = vmcp.BackendToTarget(backend)
		}
	}

	// Add prompts to routing table
	for _, prompt := range resolved.Prompts {
		backend := registry.Get(ctx, prompt.BackendID)
		if backend == nil {
			logger.Warnf("Backend %s not found in registry for prompt %s, creating minimal target",
				prompt.BackendID, prompt.Name)
			routingTable.Prompts[prompt.Name] = &vmcp.BackendTarget{
				WorkloadID: prompt.BackendID,
			}
		} else {
			routingTable.Prompts[prompt.Name] = vmcp.BackendToTarget(backend)
		}
	}

	// Create final aggregated view
	aggregated := &AggregatedCapabilities{
		Tools:            tools,
		Resources:        resolved.Resources,
		Prompts:          resolved.Prompts,
		SupportsLogging:  resolved.SupportsLogging,
		SupportsSampling: resolved.SupportsSampling,
		RoutingTable:     routingTable,
		Metadata: &AggregationMetadata{
			BackendCount:      0, // Will be set by caller
			ToolCount:         len(tools),
			ResourceCount:     len(resolved.Resources),
			PromptCount:       len(resolved.Prompts),
			ConflictsResolved: 0, // Will be tracked in future phases
		},
	}

	logger.Infof("Merged capabilities: %d tools, %d resources, %d prompts",
		aggregated.Metadata.ToolCount, aggregated.Metadata.ResourceCount, aggregated.Metadata.PromptCount)

	return aggregated, nil
}

// AggregateCapabilities is a convenience method that performs the full aggregation pipeline:
// 1. Create backend registry
// 2. Query all backends
// 3. Resolve conflicts
// 4. Merge into final view with full backend information
func (a *defaultAggregator) AggregateCapabilities(ctx context.Context, backends []vmcp.Backend) (*AggregatedCapabilities, error) {
	logger.Infof("Starting capability aggregation for %d backends", len(backends))

	// Step 1: Create registry from discovered backends
	registry := vmcp.NewImmutableRegistry(backends)
	logger.Debugf("Created backend registry with %d backends", registry.Count())

	// Step 2: Query all backends
	capabilities, err := a.QueryAllCapabilities(ctx, backends)
	if err != nil {
		return nil, fmt.Errorf("failed to query backends: %w", err)
	}

	// Step 3: Resolve conflicts
	resolved, err := a.ResolveConflicts(ctx, capabilities)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve conflicts: %w", err)
	}

	// Step 4: Merge into final view with full backend information
	aggregated, err := a.MergeCapabilities(ctx, resolved, registry)
	if err != nil {
		return nil, fmt.Errorf("failed to merge capabilities: %w", err)
	}

	// Update metadata with backend count
	aggregated.Metadata.BackendCount = len(backends)

	logger.Infof("Capability aggregation complete: %d backends, %d tools, %d resources, %d prompts",
		aggregated.Metadata.BackendCount, aggregated.Metadata.ToolCount,
		aggregated.Metadata.ResourceCount, aggregated.Metadata.PromptCount)

	return aggregated, nil
}
