// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// PriorityConflictResolver implements priority-based conflict resolution.
// The first backend in the priority order wins; conflicting tools from
// lower-priority backends are dropped.
//
// For backends not in the priority list, conflicts are resolved using
// prefix strategy as a fallback (prevents data loss).
type PriorityConflictResolver struct {
	// PriorityOrder defines the priority of backends (first has highest priority).
	PriorityOrder []string

	// priorityMap is a map from backend ID to its priority index.
	priorityMap map[string]int

	// prefixResolver is used as fallback for backends not in priority list.
	prefixResolver *PrefixConflictResolver
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
		PriorityOrder:  priorityOrder,
		priorityMap:    priorityMap,
		prefixResolver: NewPrefixConflictResolver("{workload}_"), // Fallback for unmapped backends
	}, nil
}

// ResolveToolConflicts applies priority strategy to resolve conflicts.
// Returns a map of resolved tool names to ResolvedTool structs.
func (r *PriorityConflictResolver) ResolveToolConflicts(
	_ context.Context,
	toolsByBackend map[string][]vmcp.Tool,
) (map[string]*ResolvedTool, error) {
	slog.Debug("Resolving conflicts using priority strategy", "order", r.PriorityOrder)

	resolved := make(map[string]*ResolvedTool)
	droppedTools := 0

	// First pass: collect all tools grouped by name
	toolsByName := groupToolsByName(toolsByBackend)

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
			// All candidates are from backends not in priority list
			// Use prefix strategy as fallback to avoid data loss
			backendIDs := make([]string, len(candidates))
			for i, c := range candidates {
				backendIDs[i] = c.BackendID
			}
			slog.Debug("Tool exists in backends not in priority order, using prefix fallback",
				"tool", toolName, "backends", backendIDs)

			// Apply prefix strategy to these unmapped backends
			for _, candidate := range candidates {
				prefixedName := r.prefixResolver.applyPrefix(candidate.BackendID, toolName)
				resolved[prefixedName] = &ResolvedTool{
					ResolvedName:              prefixedName,
					OriginalName:              toolName,
					Description:               candidate.Tool.Description,
					InputSchema:               candidate.Tool.InputSchema,
					BackendID:                 candidate.BackendID,
					ConflictResolutionApplied: vmcp.ConflictStrategyPrefix, // Fallback used prefix
				}
			}
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
				slog.Warn("Dropped tool from backend (lower priority)",
					"tool", toolName, "backend", candidate.BackendID, "winner", winner.BackendID)
				droppedTools++
			}
		}
	}

	if droppedTools > 0 {
		slog.Info("Priority strategy resolved tools",
			"count", len(resolved), "dropped", droppedTools)
	} else {
		slog.Info("Priority strategy resolved tools", "count", len(resolved))
	}

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
