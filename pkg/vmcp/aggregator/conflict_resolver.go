// Package aggregator provides capability aggregation for Virtual MCP Server.
package aggregator

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// PrefixConflictResolver implements automatic tool name prefixing to resolve conflicts.
// All tools are prefixed with their workload identifier according to a configurable format.
type PrefixConflictResolver struct {
	// PrefixFormat defines how to format the prefix.
	// Supported placeholders:
	//   {workload} - just the workload name
	//   {workload}_ - workload with underscore
	//   {workload}. - workload with dot
	// Can also be a custom static prefix like "backend_"
	PrefixFormat string
}

// NewPrefixConflictResolver creates a new prefix-based conflict resolver.
func NewPrefixConflictResolver(prefixFormat string) *PrefixConflictResolver {
	if prefixFormat == "" {
		prefixFormat = "{workload}_" // Default format
	}
	return &PrefixConflictResolver{
		PrefixFormat: prefixFormat,
	}
}

// ResolveToolConflicts applies prefix strategy to all tools.
// Returns a map of resolved tool names to ResolvedTool structs.
func (r *PrefixConflictResolver) ResolveToolConflicts(
	_ context.Context,
	toolsByBackend map[string][]vmcp.Tool,
) (map[string]*ResolvedTool, error) {
	logger.Debugf("Resolving conflicts using prefix strategy (format: %s)", r.PrefixFormat)

	resolved := make(map[string]*ResolvedTool)
	conflictsResolved := 0

	for backendID, tools := range toolsByBackend {
		for _, tool := range tools {
			// Apply prefix to create resolved name
			resolvedName := r.applyPrefix(backendID, tool.Name)

			// Check if this resolved name is unique
			if existing, exists := resolved[resolvedName]; exists {
				// This should be extremely rare with prefixing, but handle it
				logger.Warnf("Collision after prefixing: %s from %s conflicts with %s from %s",
					resolvedName, backendID, existing.ResolvedName, existing.BackendID)
				conflictsResolved++
				continue
			}

			// Track if we actually resolved a conflict
			if tool.Name != resolvedName {
				conflictsResolved++
			}

			resolved[resolvedName] = &ResolvedTool{
				ResolvedName:              resolvedName,
				OriginalName:              tool.Name,
				Description:               tool.Description,
				InputSchema:               tool.InputSchema,
				BackendID:                 backendID,
				ConflictResolutionApplied: vmcp.ConflictStrategyPrefix,
			}
		}
	}

	logger.Infof("Prefix strategy resolved %d potential conflicts, created %d unique tools",
		conflictsResolved, len(resolved))

	return resolved, nil
}

// applyPrefix applies the configured prefix format to a tool name.
func (r *PrefixConflictResolver) applyPrefix(backendID, toolName string) string {
	prefix := r.PrefixFormat

	// Replace {workload} placeholder with actual backend ID
	prefix = strings.ReplaceAll(prefix, "{workload}", backendID)

	return prefix + toolName
}

// PriorityConflictResolver implements priority-based conflict resolution.
// The first backend in the priority order wins; conflicting tools from
// lower-priority backends are dropped.
type PriorityConflictResolver struct {
	// PriorityOrder defines the priority of backends (first has highest priority).
	PriorityOrder []string

	// priorityMap is a map from backend ID to its priority index.
	priorityMap map[string]int
}

// NewPriorityConflictResolver creates a new priority-based conflict resolver.
func NewPriorityConflictResolver(priorityOrder []string) (*PriorityConflictResolver, error) {
	if len(priorityOrder) == 0 {
		return nil, fmt.Errorf("priority order cannot be empty")
	}

	// Build priority map for O(1) lookups
	priorityMap := make(map[string]int, len(priorityOrder))
	for i, backendID := range priorityOrder {
		if backendID == "" {
			return nil, fmt.Errorf("priority order contains empty backend ID at index %d", i)
		}
		priorityMap[backendID] = i
	}

	return &PriorityConflictResolver{
		PriorityOrder: priorityOrder,
		priorityMap:   priorityMap,
	}, nil
}

// ResolveToolConflicts applies priority strategy to resolve conflicts.
// Returns a map of resolved tool names to ResolvedTool structs.
func (r *PriorityConflictResolver) ResolveToolConflicts(
	_ context.Context,
	toolsByBackend map[string][]vmcp.Tool,
) (map[string]*ResolvedTool, error) {
	logger.Debugf("Resolving conflicts using priority strategy (order: %v)", r.PriorityOrder)

	resolved := make(map[string]*ResolvedTool)
	droppedTools := 0

	// First pass: collect all tools grouped by name
	toolsByName := make(map[string][]toolWithBackend)
	for backendID, tools := range toolsByBackend {
		for _, tool := range tools {
			toolsByName[tool.Name] = append(toolsByName[tool.Name], toolWithBackend{
				Tool:      tool,
				BackendID: backendID,
			})
		}
	}

	// Second pass: resolve conflicts using priority
	for toolName, candidates := range toolsByName {
		if len(candidates) == 1 {
			// No conflict - include the tool as-is
			candidate := candidates[0]
			resolved[toolName] = &ResolvedTool{
				ResolvedName:              toolName,
				OriginalName:              toolName,
				Description:               candidate.Tool.Description,
				InputSchema:               candidate.Tool.InputSchema,
				BackendID:                 candidate.BackendID,
				ConflictResolutionApplied: vmcp.ConflictStrategyPriority,
			}
			continue
		}

		// Conflict detected - choose the highest priority backend
		winner := r.selectWinner(candidates)
		if winner == nil {
			// All candidates are from backends not in priority list - drop all of them
			// This is intentional: priority strategy requires explicit priority ordering
			backendIDs := make([]string, len(candidates))
			for i, c := range candidates {
				backendIDs[i] = c.BackendID
			}
			logger.Warnf("Tool %s exists in multiple backends %v but none are in priority order, dropping all",
				toolName, backendIDs)
			droppedTools += len(candidates)
			continue
		}

		resolved[toolName] = &ResolvedTool{
			ResolvedName:              toolName,
			OriginalName:              toolName,
			Description:               winner.Tool.Description,
			InputSchema:               winner.Tool.InputSchema,
			BackendID:                 winner.BackendID,
			ConflictResolutionApplied: vmcp.ConflictStrategyPriority,
		}

		// Log dropped tools
		for _, candidate := range candidates {
			if candidate.BackendID != winner.BackendID {
				logger.Warnf("Dropped tool %s from backend %s (lower priority than %s)",
					toolName, candidate.BackendID, winner.BackendID)
				droppedTools++
			}
		}
	}

	logger.Infof("Priority strategy: %d unique tools, %d conflicting tools dropped",
		len(resolved), droppedTools)

	return resolved, nil
}

// selectWinner chooses the tool from the highest-priority backend.
// Returns nil if none of the candidates are in the priority list.
func (r *PriorityConflictResolver) selectWinner(candidates []toolWithBackend) *toolWithBackend {
	var winner *toolWithBackend
	winnerPriority := -1

	for i := range candidates {
		candidate := &candidates[i]
		priority, exists := r.priorityMap[candidate.BackendID]
		if !exists {
			// Backend not in priority list - skip
			continue
		}

		// Lower index = higher priority
		if winnerPriority == -1 || priority < winnerPriority {
			winner = candidate
			winnerPriority = priority
		}
	}

	return winner
}

// toolWithBackend is a helper struct to track which backend a tool comes from.
type toolWithBackend struct {
	Tool      vmcp.Tool
	BackendID string
}

// ManualConflictResolver implements manual conflict resolution.
// It requires explicit overrides for ALL conflicts and fails startup if any are unresolved.
type ManualConflictResolver struct {
	// Overrides maps (backendID, originalToolName) to the resolved configuration.
	// Key format: "backendID:toolName"
	Overrides map[string]*config.ToolOverride
}

// NewManualConflictResolver creates a new manual conflict resolver.
func NewManualConflictResolver(workloadConfigs []*config.WorkloadToolConfig) (*ManualConflictResolver, error) {
	overrides := make(map[string]*config.ToolOverride)

	// Build override map from configuration
	for _, wlConfig := range workloadConfigs {
		for toolName, override := range wlConfig.Overrides {
			if override == nil {
				continue
			}
			key := fmt.Sprintf("%s:%s", wlConfig.Workload, toolName)
			overrides[key] = override
		}
	}

	return &ManualConflictResolver{
		Overrides: overrides,
	}, nil
}

// ResolveToolConflicts applies manual conflict resolution with validation.
// Returns an error if any conflicts exist without explicit overrides.
func (r *ManualConflictResolver) ResolveToolConflicts(
	_ context.Context,
	toolsByBackend map[string][]vmcp.Tool,
) (map[string]*ResolvedTool, error) {
	logger.Debugf("Resolving conflicts using manual strategy with %d overrides", len(r.Overrides))

	// Group tools by name to detect conflicts
	toolsByName := groupToolsByName(toolsByBackend)

	// Check for unresolved conflicts
	if unresolvedConflicts := r.findUnresolvedConflicts(toolsByName); len(unresolvedConflicts) > 0 {
		return nil, r.formatConflictError(unresolvedConflicts)
	}

	// Apply overrides and build resolved map
	resolved, err := r.applyOverridesAndResolve(toolsByBackend)
	if err != nil {
		return nil, err
	}

	logger.Infof("Manual strategy: %d unique tools after applying overrides", len(resolved))
	return resolved, nil
}

// groupToolsByName groups tools by their names to detect conflicts.
func groupToolsByName(toolsByBackend map[string][]vmcp.Tool) map[string][]toolWithBackend {
	toolsByName := make(map[string][]toolWithBackend)
	for backendID, tools := range toolsByBackend {
		for _, tool := range tools {
			toolsByName[tool.Name] = append(toolsByName[tool.Name], toolWithBackend{
				Tool:      tool,
				BackendID: backendID,
			})
		}
	}
	return toolsByName
}

// findUnresolvedConflicts checks for conflicts without explicit overrides.
func (r *ManualConflictResolver) findUnresolvedConflicts(toolsByName map[string][]toolWithBackend) map[string][]string {
	unresolvedConflicts := make(map[string][]string)
	for toolName, candidates := range toolsByName {
		if len(candidates) <= 1 {
			continue // No conflict
		}

		// Check if all conflicting tools have overrides
		if !r.allCandidatesHaveOverrides(toolName, candidates) {
			backendIDs := make([]string, len(candidates))
			for i, candidate := range candidates {
				backendIDs[i] = candidate.BackendID
			}
			unresolvedConflicts[toolName] = backendIDs
		}
	}
	return unresolvedConflicts
}

// allCandidatesHaveOverrides checks if all candidates for a tool have overrides configured.
func (r *ManualConflictResolver) allCandidatesHaveOverrides(toolName string, candidates []toolWithBackend) bool {
	for _, candidate := range candidates {
		key := fmt.Sprintf("%s:%s", candidate.BackendID, toolName)
		if _, hasOverride := r.Overrides[key]; !hasOverride {
			return false
		}
	}
	return true
}

// applyOverridesAndResolve applies overrides and builds the resolved tool map.
func (r *ManualConflictResolver) applyOverridesAndResolve(
	toolsByBackend map[string][]vmcp.Tool,
) (map[string]*ResolvedTool, error) {
	resolved := make(map[string]*ResolvedTool)
	for backendID, tools := range toolsByBackend {
		for _, tool := range tools {
			resolvedTool := r.resolveToolWithOverride(backendID, tool)

			// Check for collision after override
			if existing, exists := resolved[resolvedTool.ResolvedName]; exists {
				return nil, fmt.Errorf("collision after override: tool %s from backend %s conflicts with tool from backend %s",
					resolvedTool.ResolvedName, backendID, existing.BackendID)
			}

			resolved[resolvedTool.ResolvedName] = resolvedTool
		}
	}
	return resolved, nil
}

// resolveToolWithOverride applies overrides to a single tool.
func (r *ManualConflictResolver) resolveToolWithOverride(backendID string, tool vmcp.Tool) *ResolvedTool {
	resolvedName := tool.Name
	description := tool.Description

	// Check if there's an override for this tool
	key := fmt.Sprintf("%s:%s", backendID, tool.Name)
	if override, exists := r.Overrides[key]; exists {
		if override.Name != "" {
			resolvedName = override.Name
		}
		if override.Description != "" {
			description = override.Description
		}
	}

	return &ResolvedTool{
		ResolvedName:              resolvedName,
		OriginalName:              tool.Name,
		Description:               description,
		InputSchema:               tool.InputSchema,
		BackendID:                 backendID,
		ConflictResolutionApplied: vmcp.ConflictStrategyManual,
	}
}

// formatConflictError creates a detailed error message for unresolved conflicts.
func (*ManualConflictResolver) formatConflictError(conflicts map[string][]string) error {
	var sb strings.Builder
	sb.WriteString("unresolved tool name conflicts detected:\n")

	for toolName, backendIDs := range conflicts {
		sb.WriteString(fmt.Sprintf("  - %s: [%s]\n", toolName, strings.Join(backendIDs, ", ")))
	}

	sb.WriteString("\nUse 'overrides' in aggregation config to resolve these conflicts when using conflict_resolution: manual")

	return fmt.Errorf("%w: %s", ErrUnresolvedConflicts, sb.String())
}

// NewConflictResolver creates the appropriate conflict resolver based on configuration.
func NewConflictResolver(aggregationConfig *config.AggregationConfig) (ConflictResolver, error) {
	if aggregationConfig == nil {
		// Default to prefix strategy with default format
		logger.Infof("No aggregation config provided, using default prefix strategy")
		return NewPrefixConflictResolver("{workload}_"), nil
	}

	switch aggregationConfig.ConflictResolution {
	case vmcp.ConflictStrategyPrefix:
		prefixFormat := "{workload}_" // Default
		if aggregationConfig.ConflictResolutionConfig != nil &&
			aggregationConfig.ConflictResolutionConfig.PrefixFormat != "" {
			prefixFormat = aggregationConfig.ConflictResolutionConfig.PrefixFormat
		}
		logger.Infof("Using prefix conflict resolution strategy (format: %s)", prefixFormat)
		return NewPrefixConflictResolver(prefixFormat), nil

	case vmcp.ConflictStrategyPriority:
		if aggregationConfig.ConflictResolutionConfig == nil ||
			len(aggregationConfig.ConflictResolutionConfig.PriorityOrder) == 0 {
			return nil, fmt.Errorf("priority strategy requires priority_order in conflict_resolution_config")
		}
		logger.Infof("Using priority conflict resolution strategy (order: %v)",
			aggregationConfig.ConflictResolutionConfig.PriorityOrder)
		return NewPriorityConflictResolver(aggregationConfig.ConflictResolutionConfig.PriorityOrder)

	case vmcp.ConflictStrategyManual:
		logger.Infof("Using manual conflict resolution strategy")
		return NewManualConflictResolver(aggregationConfig.Tools)

	default:
		return nil, fmt.Errorf("%w: %s", ErrInvalidConflictStrategy, aggregationConfig.ConflictResolution)
	}
}
