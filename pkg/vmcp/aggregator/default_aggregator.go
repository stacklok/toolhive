// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sort"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"golang.org/x/sync/errgroup"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// defaultAggregator implements the Aggregator interface for capability aggregation.
// It queries backends in parallel, handles failures gracefully, and merges capabilities.
type defaultAggregator struct {
	backendClient    vmcp.BackendClient
	conflictResolver ConflictResolver
	toolConfigMap    map[string]*config.WorkloadToolConfig // Maps backend ID to tool config
	excludeAllTools  bool                                  // Global flag to exclude all tools
	// promptNaming controls how advertised prompt names are formed (see
	// resolvePromptConflicts). Derived from the aggregation config at
	// construction.
	promptNaming promptNaming
	tracer       trace.Tracer
}

// NewDefaultAggregator creates a new default aggregator implementation.
// conflictResolver handles tool name conflicts across backends.
// aggregationConfig specifies aggregation settings including tool filtering/overrides and excludeAllTools.
// tracerProvider is used to create a tracer for distributed tracing (pass nil for no tracing).
func NewDefaultAggregator(
	backendClient vmcp.BackendClient,
	conflictResolver ConflictResolver,
	aggregationConfig *config.AggregationConfig,
	tracerProvider trace.TracerProvider,
) Aggregator {
	// Build tool config map for quick lookup by backend ID
	toolConfigMap := make(map[string]*config.WorkloadToolConfig)
	var excludeAllTools bool

	if aggregationConfig != nil {
		excludeAllTools = aggregationConfig.ExcludeAllTools
		for _, wlConfig := range aggregationConfig.Tools {
			if wlConfig != nil {
				toolConfigMap[wlConfig.Workload] = wlConfig
			}
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
		excludeAllTools:  excludeAllTools,
		promptNaming:     promptNamingFromConfig(aggregationConfig),
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

	slog.Debug("querying capabilities from backend", "backend", backend.ID)

	// Create a BackendTarget from the Backend
	// Use BackendToTarget helper to ensure all fields (including auth) are copied
	target := vmcp.BackendToTarget(&backend)

	// Query capabilities using the backend client
	capabilities, err := a.backendClient.ListCapabilities(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrBackendQueryFailed, backend.ID, err)
	}

	// Apply per-backend tool overrides (before conflict resolution)
	// NOTE: ExcludeAll and Filter are NOT applied here. This is intentional -
	// we need all tools in the routing table so composite tools can call backend
	// tools. ExcludeAll and Filter are applied in MergeCapabilities (via
	// shouldAdvertiseTool) to control which tools are advertised to MCP clients.
	processedTools := processBackendTools(ctx, backend.ID, capabilities.Tools, a.toolConfigMap[backend.ID])

	// Convert to BackendCapabilities
	result := &BackendCapabilities{
		BackendID:         backend.ID,
		Tools:             processedTools,
		Resources:         capabilities.Resources,
		ResourceTemplates: capabilities.ResourceTemplates,
		Prompts:           capabilities.Prompts,
		SupportsLogging:   capabilities.SupportsLogging,
		SupportsSampling:  capabilities.SupportsSampling,
	}

	span.SetAttributes(
		attribute.Int("tools.count", len(result.Tools)),
		attribute.Int("resources.count", len(result.Resources)),
		attribute.Int("prompts.count", len(result.Prompts)),
	)

	slog.Debug("backend capabilities queried",
		"backend", backend.ID, "tools", len(result.Tools), "resources", len(result.Resources), "prompts", len(result.Prompts))

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

	slog.Info("querying capabilities from backends", "count", len(backends))

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
				slog.Warn("failed to query backend", "backend", backend.ID, "error", err)
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

	slog.Info("successfully queried backends", "successful", len(capabilities), "total", len(backends))
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

	slog.Debug("resolving conflicts across backends", "count", len(capabilities))

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
		// Fallback: no conflict resolution (first wins in sorted backend
		// order, so the winner is deterministic; log warnings)
		slog.Warn("no conflict resolver configured, using fallback (first wins)")
		resolvedTools = make(map[string]*ResolvedTool)
		for _, backendID := range slices.Sorted(maps.Keys(toolsByBackend)) {
			for _, tool := range toolsByBackend[backendID] {
				if existing, exists := resolvedTools[tool.Name]; exists {
					slog.Warn("tool name conflict, keeping first",
						"tool", tool.Name, "existing_backend", existing.BackendID, "conflicting_backend", backendID)
					continue
				}
				resolvedTools[tool.Name] = &ResolvedTool{
					ResolvedName: tool.Name,
					OriginalName: tool.Name,
					Description:  tool.Description,
					InputSchema:  tool.InputSchema,
					OutputSchema: tool.OutputSchema,
					Annotations:  tool.Annotations,
					BackendID:    backendID,
				}
			}
		}
	}

	// Build resolved capabilities
	resolved := &ResolvedCapabilities{
		Tools: resolvedTools,
	}

	// Resolve conflicts for resources, resource templates, and prompts.
	// Backends are iterated in sorted-ID order so that collision outcomes are
	// deterministic across runs (map iteration order is not). See
	// capability_conflicts.go for the per-type policies.
	backendIDs := slices.Sorted(maps.Keys(capabilities))
	resolved.Resources = resolveResourceConflicts(backendIDs, capabilities)
	resolved.ResourceTemplates = resolveResourceTemplateConflicts(backendIDs, capabilities)
	resolved.Prompts, err = resolvePromptConflicts(a.promptNaming, backendIDs, capabilities)
	if err != nil {
		return nil, fmt.Errorf("prompt conflict resolution failed: %w", err)
	}

	for _, caps := range capabilities {
		// Aggregate logging/sampling support (OR logic - enabled if any backend supports)
		resolved.SupportsLogging = resolved.SupportsLogging || caps.SupportsLogging
		resolved.SupportsSampling = resolved.SupportsSampling || caps.SupportsSampling
	}

	span.SetAttributes(
		attribute.Int("resolved.tools", len(resolved.Tools)),
		attribute.Int("resolved.resources", len(resolved.Resources)),
		attribute.Int("resolved.prompts", len(resolved.Prompts)),
	)

	slog.Debug("resolved capabilities",
		"tools", len(resolved.Tools), "resources", len(resolved.Resources), "prompts", len(resolved.Prompts))

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

	slog.Debug("merging capabilities into final view")

	// Create routing table
	routingTable := &vmcp.RoutingTable{
		Tools:             make(map[string]*vmcp.BackendTarget),
		Resources:         make(map[string]*vmcp.BackendTarget),
		ResourceTemplates: make(map[string]*vmcp.BackendTarget),
		Prompts:           make(map[string]*vmcp.BackendTarget),
	}

	// Convert resolved tools to final vmcp.Tool format
	// The routing table gets ALL tools (for composite tool routing)
	// The advertised tools list only gets non-excluded/non-filtered tools (for MCP clients)
	tools := make([]vmcp.Tool, 0, len(resolved.Tools))
	for _, resolvedTool := range resolved.Tools {
		// Check if this tool should be excluded from the advertised list
		// ExcludeAll and Filter only affect advertising, not routing
		shouldAdvertise := a.shouldAdvertiseTool(resolvedTool.BackendID, resolvedTool.OriginalName)

		if shouldAdvertise {
			tools = append(tools, vmcp.Tool{
				Name:         resolvedTool.ResolvedName,
				Description:  resolvedTool.Description,
				InputSchema:  resolvedTool.InputSchema,
				OutputSchema: resolvedTool.OutputSchema,
				Annotations:  resolvedTool.Annotations,
				BackendID:    resolvedTool.BackendID,
			})
		}

		// ALWAYS add to routing table (for composite tools to call excluded backend tools)
		// Look up full backend information from registry
		backend := registry.Get(ctx, resolvedTool.BackendID)
		if backend == nil {
			slog.Warn("backend not found in registry for tool, creating minimal target",
				"backend", resolvedTool.BackendID, "tool", resolvedTool.ResolvedName)
			routingTable.Tools[resolvedTool.ResolvedName] = &vmcp.BackendTarget{
				WorkloadID:             resolvedTool.BackendID,
				OriginalCapabilityName: actualBackendCapabilityName(a.toolConfigMap, resolvedTool.BackendID, resolvedTool.OriginalName),
			}
		} else {
			// Use the backendToTarget helper from registry package
			target := vmcp.BackendToTarget(backend)
			// Store the actual backend capability name for forwarding to backend.
			// resolvedTool.OriginalName is the post-override name; reverse the override
			// to get the name the backend itself uses.
			target.OriginalCapabilityName = actualBackendCapabilityName(a.toolConfigMap, resolvedTool.BackendID, resolvedTool.OriginalName)
			routingTable.Tools[resolvedTool.ResolvedName] = target
		}
	}

	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name < tools[j].Name
	})

	// Add resources, resource templates, and prompts to the routing table.
	// ResolveConflicts is the enforcement point that makes their identities
	// unique (see the capability_conflicts.go header), but Merge is
	// independently callable, so as defence in depth each merge helper keeps
	// a duplicate out of both the routing table and the advertised list
	// (first wins, loudly) rather than silently overwriting the earlier
	// routing entry.
	resources := mergeResources(ctx, resolved.Resources, registry, routingTable)
	templates := mergeResourceTemplates(ctx, resolved.ResourceTemplates, registry, routingTable)
	prompts := mergePrompts(ctx, resolved.Prompts, registry, routingTable)

	// Determine conflict strategy used
	conflictStrategy := vmcp.ConflictStrategyPrefix // Default
	if len(resolved.Tools) > 0 {
		// Get strategy from first tool (all tools use same strategy)
		for _, tool := range resolved.Tools {
			conflictStrategy = tool.ConflictResolutionApplied
			break
		}
	}

	// Create final aggregated view. The advertised lists are the ones built
	// alongside the routing tables above, so advertising and routing always
	// agree on which entry owns a duplicated identity.
	aggregated := &AggregatedCapabilities{
		Tools:             tools,
		Resources:         resources,
		ResourceTemplates: templates,
		Prompts:           prompts,
		SupportsLogging:   resolved.SupportsLogging,
		SupportsSampling:  resolved.SupportsSampling,
		RoutingTable:      routingTable,
		Metadata: &AggregationMetadata{
			BackendCount:          0, // Will be set by caller
			ToolCount:             len(tools),
			ResourceCount:         len(resources),
			ResourceTemplateCount: len(templates),
			PromptCount:           len(prompts),
			ConflictStrategy:      conflictStrategy,
		},
	}

	span.SetAttributes(
		attribute.Int("aggregated.tools", aggregated.Metadata.ToolCount),
		attribute.Int("aggregated.resources", aggregated.Metadata.ResourceCount),
		attribute.Int("aggregated.prompts", aggregated.Metadata.PromptCount),
		attribute.String("conflict.strategy", string(aggregated.Metadata.ConflictStrategy)),
	)

	slog.Info("merged capabilities",
		"tools", aggregated.Metadata.ToolCount,
		"resources", aggregated.Metadata.ResourceCount,
		"prompts", aggregated.Metadata.PromptCount)

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

	slog.Info("starting capability aggregation", "backends", len(backends))

	// Step 1: Create registry from discovered backends
	registry := vmcp.NewImmutableRegistry(backends)
	slog.Debug("created backend registry", "count", registry.Count())

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

	slog.Info("capability aggregation complete",
		"backends", aggregated.Metadata.BackendCount, "tools", aggregated.Metadata.ToolCount,
		"resources", aggregated.Metadata.ResourceCount, "prompts", aggregated.Metadata.PromptCount)

	return aggregated, nil
}

// mergeResources adds resources to the routing table (keyed by URI) and
// returns the advertised resource list. A URI already present in the routing
// table is dropped from both, with a warning (first wins), so advertising and
// routing always agree on which backend owns a URI.
func mergeResources(
	ctx context.Context,
	resolved []vmcp.Resource,
	registry vmcp.BackendRegistry,
	routingTable *vmcp.RoutingTable,
) []vmcp.Resource {
	resources := make([]vmcp.Resource, 0, len(resolved))
	for _, resource := range resolved {
		if existing, duplicate := routingTable.Resources[resource.URI]; duplicate {
			slog.Warn("duplicate resource URI in resolved capabilities, keeping first",
				"uri", resource.URI, "kept_backend", existing.WorkloadID, "dropped_backend", resource.BackendID)
			continue
		}
		resources = append(resources, resource)
		backend := registry.Get(ctx, resource.BackendID)
		if backend == nil {
			slog.Warn("backend not found in registry for resource, creating minimal target",
				"backend", resource.BackendID, "resource", resource.URI)
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
	return resources
}

// mergeResourceTemplates adds resource templates to the routing table (keyed
// by URI-template string) and returns the advertised template list.
// Pass-through, mirroring resources: no URI-template rewriting, and the same
// first-wins guard against duplicate template strings.
//
// OriginalCapabilityName is intentionally left empty. A resources/read routed
// via a template carries the client's CONCRETE, already-expanded URI (e.g.
// file:///logs/2025-01-01.txt); the backend performs its own template
// expansion, so that concrete URI must reach it verbatim. Setting
// OriginalCapabilityName to the template string would make
// GetBackendCapabilityName replace the concrete URI with the unexpanded
// template, and the backend would return unsubstituted content. vMCP does not
// rename templates, so no name translation is needed here.
func mergeResourceTemplates(
	ctx context.Context,
	resolved []vmcp.ResourceTemplate,
	registry vmcp.BackendRegistry,
	routingTable *vmcp.RoutingTable,
) []vmcp.ResourceTemplate {
	templates := make([]vmcp.ResourceTemplate, 0, len(resolved))
	for _, template := range resolved {
		if existing, duplicate := routingTable.ResourceTemplates[template.URITemplate]; duplicate {
			slog.Warn("duplicate resource template in resolved capabilities, keeping first",
				"resource_template", template.URITemplate,
				"kept_backend", existing.WorkloadID, "dropped_backend", template.BackendID)
			continue
		}
		templates = append(templates, template)
		backend := registry.Get(ctx, template.BackendID)
		if backend == nil {
			slog.Warn("backend not found in registry for resource template, creating minimal target",
				"backend", template.BackendID, "resource_template", template.URITemplate)
			routingTable.ResourceTemplates[template.URITemplate] = &vmcp.BackendTarget{
				WorkloadID: template.BackendID,
			}
		} else {
			routingTable.ResourceTemplates[template.URITemplate] = vmcp.BackendToTarget(backend)
		}
	}
	return templates
}

// mergePrompts adds prompts to the routing table, keyed by resolved
// (advertised) name, and returns the advertised prompt list, with the same
// first-wins guard against duplicate resolved names.
func mergePrompts(
	ctx context.Context,
	resolved []ResolvedPrompt,
	registry vmcp.BackendRegistry,
	routingTable *vmcp.RoutingTable,
) []vmcp.Prompt {
	prompts := make([]vmcp.Prompt, 0, len(resolved))
	for _, prompt := range resolved {
		if existing, duplicate := routingTable.Prompts[prompt.Name]; duplicate {
			slog.Warn("duplicate prompt name in resolved capabilities, keeping first",
				"prompt", prompt.Name, "kept_backend", existing.WorkloadID, "dropped_backend", prompt.BackendID)
			continue
		}
		prompts = append(prompts, prompt.Prompt)
		backend := registry.Get(ctx, prompt.BackendID)
		if backend == nil {
			slog.Warn("backend not found in registry for prompt, creating minimal target",
				"backend", prompt.BackendID, "prompt", prompt.Name)
			routingTable.Prompts[prompt.Name] = &vmcp.BackendTarget{
				WorkloadID:             prompt.BackendID,
				OriginalCapabilityName: prompt.OriginalName,
			}
		} else {
			target := vmcp.BackendToTarget(backend)
			// Store the backend's own prompt name so prompts/get on a renamed
			// prompt forwards the name the backend actually knows.
			target.OriginalCapabilityName = prompt.OriginalName
			routingTable.Prompts[prompt.Name] = target
		}
	}
	return prompts
}

// actualBackendCapabilityName returns the real capability name the backend uses,
// reversing any per-backend override rename that processBackendTools may have applied.
//
// processBackendTools renames tools when WorkloadToolConfig.Overrides maps an original
// backend name to a user-visible name. The conflict resolvers receive the post-override
// name and store it as ResolvedTool.OriginalName. Setting OriginalCapabilityName to that
// value would forward the overridden (user-visible) name to the backend, which only knows
// the original name.
//
// Returns postOverrideName unchanged when no matching override is configured.
func actualBackendCapabilityName(toolConfigMap map[string]*config.WorkloadToolConfig, backendID, postOverrideName string) string {
	wlConfig, ok := toolConfigMap[backendID]
	if !ok || wlConfig == nil {
		return postOverrideName
	}
	for origName, override := range wlConfig.Overrides {
		if override != nil && override.Name == postOverrideName {
			return origName
		}
	}
	return postOverrideName
}

// shouldAdvertiseTool returns true if a tool from the given backend should be
// advertised to MCP clients (included in tools/list response).
//
// ExcludeAll, Filter, and per-workload settings control advertising, not routing:
// - Tools excluded via ExcludeAll are NOT advertised to MCP clients
// - Tools not matching Filter are NOT advertised to MCP clients
// - BUT they ARE available in the routing table for composite tools to use
//
// This enables the use case where you want to hide raw backend tools from
// direct client access while still allowing curated composite workflows to use them.
//
// Parameters:
//   - backendID: The ID of the backend that owns the tool
//   - originalToolName: The original tool name (before overrides) for filter matching
func (a *defaultAggregator) shouldAdvertiseTool(backendID, originalToolName string) bool {
	// Global ExcludeAllTools takes precedence - excludes all tools from all backends
	if a.excludeAllTools {
		return false
	}

	// Check per-workload settings
	wlConfig, exists := a.toolConfigMap[backendID]
	if !exists {
		// No config for this backend, advertise the tool
		return true
	}

	// Check per-workload ExcludeAll setting
	if wlConfig.ExcludeAll {
		return false
	}

	// Check per-workload Filter setting
	// Filter is a positive list - if non-empty, only tools matching the filter are advertised
	if len(wlConfig.Filter) > 0 {
		for _, allowedTool := range wlConfig.Filter {
			if allowedTool == originalToolName {
				return true // Tool matches filter, advertise it
			}
		}
		// Tool doesn't match any filter entry, don't advertise
		return false
	}

	// No filter configured, advertise the tool
	return true
}
