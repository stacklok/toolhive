// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"golang.org/x/sync/errgroup"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// defaultAggregator implements the Aggregator interface for capability aggregation.
// It queries backends in parallel, handles failures gracefully, and merges capabilities.
type defaultAggregator struct {
	backendClient    vmcp.BackendClient
	conflictResolver ConflictResolver
	toolConfigMap    map[string]*config.WorkloadToolConfig // Maps backend ID to tool config
	tracer           trace.Tracer
}

// NewDefaultAggregator creates a new default aggregator implementation.
// conflictResolver handles tool name conflicts across backends.
// workloadConfigs specifies per-backend tool filtering and overrides.
// tracerProvider is used to create a tracer for distributed tracing (pass nil for no tracing).
func NewDefaultAggregator(
	backendClient vmcp.BackendClient,
	conflictResolver ConflictResolver,
	workloadConfigs []*config.WorkloadToolConfig,
	tracerProvider trace.TracerProvider,
) Aggregator {
	// Build tool config map for quick lookup by backend ID
	toolConfigMap := make(map[string]*config.WorkloadToolConfig)
	for _, wlConfig := range workloadConfigs {
		if wlConfig != nil {
			toolConfigMap[wlConfig.Workload] = wlConfig
		}
	}

	// Create tracer from provider (use noop tracer if provider is nil)
	var tracer trace.Tracer
	if tracerProvider != nil {
		tracer = tracerProvider.Tracer("github.com/stacklok/toolhive/pkg/vmcp/aggregator")
	} else {
		tracer = noop.NewTracerProvider().Tracer("github.com/stacklok/toolhive/pkg/vmcp/aggregator")
	}

	return &defaultAggregator{
		backendClient:    backendClient,
		conflictResolver: conflictResolver,
		toolConfigMap:    toolConfigMap,
		tracer:           tracer,
	}
}

// QueryCapabilities queries a single backend for its MCP capabilities.
// Returns the raw capabilities (tools, resources, prompts) from the backend.
func (a *defaultAggregator) QueryCapabilities(ctx context.Context, backend vmcp.Backend) (_ *BackendCapabilities, retErr error) {
	ctx, span := a.tracer.Start(ctx, "aggregator.QueryCapabilities",
		trace.WithAttributes(
			attribute.String("backend.id", backend.ID),
		),
	)
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	logger.Debugf("Querying capabilities from backend %s", backend.ID)

	// Create a BackendTarget from the Backend
	// Use BackendToTarget helper to ensure all fields (including auth) are copied
	target := vmcp.BackendToTarget(&backend)

	// Query capabilities using the backend client
	capabilities, err := a.backendClient.ListCapabilities(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrBackendQueryFailed, backend.ID, err)
	}

	// Apply per-backend tool filtering and overrides (before conflict resolution)
	processedTools := processBackendTools(ctx, backend.ID, capabilities.Tools, a.toolConfigMap[backend.ID])

	// Convert to BackendCapabilities
	result := &BackendCapabilities{
		BackendID:        backend.ID,
		Tools:            processedTools,
		Resources:        capabilities.Resources,
		Prompts:          capabilities.Prompts,
		SupportsLogging:  capabilities.SupportsLogging,
		SupportsSampling: capabilities.SupportsSampling,
	}

	span.SetAttributes(
		attribute.Int("tools.count", len(result.Tools)),
		attribute.Int("resources.count", len(result.Resources)),
		attribute.Int("prompts.count", len(result.Prompts)),
	)

	logger.Debugf("Backend %s: %d tools (after filtering/overrides), %d resources, %d prompts",
		backend.ID, len(result.Tools), len(result.Resources), len(result.Prompts))

	return result, nil
}

// QueryAllCapabilities queries all backends for their capabilities in parallel.
// Handles backend failures gracefully (logs and continues with remaining backends).
func (a *defaultAggregator) QueryAllCapabilities(
	ctx context.Context,
	backends []vmcp.Backend,
) (_ map[string]*BackendCapabilities, retErr error) {
	ctx, span := a.tracer.Start(ctx, "aggregator.QueryAllCapabilities",
		trace.WithAttributes(
			attribute.Int("backends.count", len(backends)),
		),
	)
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

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

	span.SetAttributes(
		attribute.Int("successful.backends", len(capabilities)),
	)

	logger.Infof("Successfully queried %d/%d backends", len(capabilities), len(backends))
	return capabilities, nil
}

// ResolveConflicts applies conflict resolution strategy to handle
// duplicate capability names across backends.
func (a *defaultAggregator) ResolveConflicts(
	ctx context.Context,
	capabilities map[string]*BackendCapabilities,
) (_ *ResolvedCapabilities, retErr error) {
	ctx, span := a.tracer.Start(ctx, "aggregator.ResolveConflicts",
		trace.WithAttributes(
			attribute.Int("backends.count", len(capabilities)),
		),
	)
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	logger.Debugf("Resolving conflicts across %d backends", len(capabilities))

	// Group tools by backend for conflict resolution
	toolsByBackend := make(map[string][]vmcp.Tool)
	for backendID, caps := range capabilities {
		toolsByBackend[backendID] = caps.Tools
	}

	// Use the configured conflict resolver to resolve tool conflicts
	var resolvedTools map[string]*ResolvedTool
	var err error

	if a.conflictResolver != nil {
		resolvedTools, err = a.conflictResolver.ResolveToolConflicts(ctx, toolsByBackend)
		if err != nil {
			return nil, fmt.Errorf("conflict resolution failed: %w", err)
		}
	} else {
		// Fallback: no conflict resolution (first wins, log warnings)
		logger.Warnf("No conflict resolver configured, using fallback (first wins)")
		resolvedTools = make(map[string]*ResolvedTool)
		for backendID, tools := range toolsByBackend {
			for _, tool := range tools {
				if existing, exists := resolvedTools[tool.Name]; exists {
					logger.Warnf("Tool name conflict: %s exists in both %s and %s (keeping first)",
						tool.Name, existing.BackendID, backendID)
					continue
				}
				resolvedTools[tool.Name] = &ResolvedTool{
					ResolvedName: tool.Name,
					OriginalName: tool.Name,
					Description:  tool.Description,
					InputSchema:  tool.InputSchema,
					BackendID:    backendID,
				}
			}
		}
	}

	// Build resolved capabilities
	resolved := &ResolvedCapabilities{
		Tools:     resolvedTools,
		Resources: []vmcp.Resource{},
		Prompts:   []vmcp.Prompt{},
	}

	// Collect resources and prompts (no conflict resolution for these yet)
	for _, caps := range capabilities {
		resolved.Resources = append(resolved.Resources, caps.Resources...)
		resolved.Prompts = append(resolved.Prompts, caps.Prompts...)

		// Aggregate logging/sampling support (OR logic - enabled if any backend supports)
		resolved.SupportsLogging = resolved.SupportsLogging || caps.SupportsLogging
		resolved.SupportsSampling = resolved.SupportsSampling || caps.SupportsSampling
	}

	span.SetAttributes(
		attribute.Int("resolved.tools", len(resolved.Tools)),
		attribute.Int("resolved.resources", len(resolved.Resources)),
		attribute.Int("resolved.prompts", len(resolved.Prompts)),
	)

	logger.Debugf("Resolved %d unique tools, %d resources, %d prompts",
		len(resolved.Tools), len(resolved.Resources), len(resolved.Prompts))

	return resolved, nil
}

// MergeCapabilities creates the final unified capability view and routing table.
// Uses the backend registry to populate full BackendTarget information for routing.
func (a *defaultAggregator) MergeCapabilities(
	ctx context.Context,
	resolved *ResolvedCapabilities,
	registry vmcp.BackendRegistry,
) (_ *AggregatedCapabilities, retErr error) {
	ctx, span := a.tracer.Start(ctx, "aggregator.MergeCapabilities",
		trace.WithAttributes(
			attribute.Int("resolved.tools", len(resolved.Tools)),
			attribute.Int("resolved.resources", len(resolved.Resources)),
			attribute.Int("resolved.prompts", len(resolved.Prompts)),
		),
	)
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

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
				WorkloadID:             resolvedTool.BackendID,
				OriginalCapabilityName: resolvedTool.OriginalName,
			}
		} else {
			// Use the backendToTarget helper from registry package
			target := vmcp.BackendToTarget(backend)
			// Store the original tool name for forwarding to backend
			target.OriginalCapabilityName = resolvedTool.OriginalName
			routingTable.Tools[resolvedTool.ResolvedName] = target
		}
	}

	// Add resources to routing table
	for _, resource := range resolved.Resources {
		backend := registry.Get(ctx, resource.BackendID)
		if backend == nil {
			logger.Warnf("Backend %s not found in registry for resource %s, creating minimal target",
				resource.BackendID, resource.URI)
			routingTable.Resources[resource.URI] = &vmcp.BackendTarget{
				WorkloadID:             resource.BackendID,
				OriginalCapabilityName: resource.URI,
			}
		} else {
			target := vmcp.BackendToTarget(backend)
			// Store the original resource URI for forwarding to backend
			target.OriginalCapabilityName = resource.URI
			routingTable.Resources[resource.URI] = target
		}
	}

	// Add prompts to routing table
	for _, prompt := range resolved.Prompts {
		backend := registry.Get(ctx, prompt.BackendID)
		if backend == nil {
			logger.Warnf("Backend %s not found in registry for prompt %s, creating minimal target",
				prompt.BackendID, prompt.Name)
			routingTable.Prompts[prompt.Name] = &vmcp.BackendTarget{
				WorkloadID:             prompt.BackendID,
				OriginalCapabilityName: prompt.Name,
			}
		} else {
			target := vmcp.BackendToTarget(backend)
			// Store the original prompt name for forwarding to backend
			target.OriginalCapabilityName = prompt.Name
			routingTable.Prompts[prompt.Name] = target
		}
	}

	// Determine conflict strategy used
	conflictStrategy := vmcp.ConflictStrategyPrefix // Default
	if len(resolved.Tools) > 0 {
		// Get strategy from first tool (all tools use same strategy)
		for _, tool := range resolved.Tools {
			conflictStrategy = tool.ConflictResolutionApplied
			break
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
			BackendCount:     0, // Will be set by caller
			ToolCount:        len(tools),
			ResourceCount:    len(resolved.Resources),
			PromptCount:      len(resolved.Prompts),
			ConflictStrategy: conflictStrategy,
		},
	}

	span.SetAttributes(
		attribute.Int("aggregated.tools", aggregated.Metadata.ToolCount),
		attribute.Int("aggregated.resources", aggregated.Metadata.ResourceCount),
		attribute.Int("aggregated.prompts", aggregated.Metadata.PromptCount),
		attribute.String("conflict.strategy", string(aggregated.Metadata.ConflictStrategy)),
	)

	logger.Infof("Merged capabilities: %d tools, %d resources, %d prompts",
		aggregated.Metadata.ToolCount, aggregated.Metadata.ResourceCount, aggregated.Metadata.PromptCount)

	return aggregated, nil
}

// AggregateCapabilities is a convenience method that performs the full aggregation pipeline:
// 1. Create backend registry
// 2. Query all backends
// 3. Resolve conflicts
// 4. Merge into final view with full backend information
func (a *defaultAggregator) AggregateCapabilities(
	ctx context.Context,
	backends []vmcp.Backend,
) (_ *AggregatedCapabilities, retErr error) {
	ctx, span := a.tracer.Start(ctx, "aggregator.AggregateCapabilities",
		trace.WithAttributes(
			attribute.Int("backends.count", len(backends)),
		),
	)
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

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

	span.SetAttributes(
		attribute.Int("aggregated.backends", aggregated.Metadata.BackendCount),
		attribute.Int("aggregated.tools", aggregated.Metadata.ToolCount),
		attribute.Int("aggregated.resources", aggregated.Metadata.ResourceCount),
		attribute.Int("aggregated.prompts", aggregated.Metadata.PromptCount),
		attribute.String("conflict.strategy", string(aggregated.Metadata.ConflictStrategy)),
	)

	logger.Infof("Capability aggregation complete: %d backends, %d tools, %d resources, %d prompts",
		aggregated.Metadata.BackendCount, aggregated.Metadata.ToolCount,
		aggregated.Metadata.ResourceCount, aggregated.Metadata.PromptCount)

	return aggregated, nil
}
