// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// ManualConflictResolver implements manual conflict resolution.
// It requires explicit overrides for ALL conflicts and fails startup if any are unresolved.
type ManualConflictResolver struct {
	// Overrides maps (backendID, originalToolName) to the resolved configuration.
	// Key format: "backendID:toolName"
	Overrides map[string]*config.ToolOverride
}

// NewManualConflictResolver creates a new manual conflict resolver.
// Note: This resolver validates that overrides don't create NEW conflicts.
// If two tools are both overridden to the same name, ResolveToolConflicts
// will return an error ("collision after override").
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
		fmt.Fprintf(&sb, "  - %s: [%s]\n", toolName, strings.Join(backendIDs, ", "))
	}

	sb.WriteString("\nUse 'overrides' in aggregation config to resolve these conflicts when using conflict_resolution: manual")

	return fmt.Errorf("%w: %s", ErrUnresolvedConflicts, sb.String())
}
