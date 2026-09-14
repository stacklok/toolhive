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
// When every conflicting backend is listed in the priority order, the first
// backend in that order wins and lower-priority tools are dropped.
//
// When any conflicting backend is absent from the priority list, all candidates
// in that conflict are dropped because the conflict cannot be safely ranked.
// Dropping preserves the fail-closed policy invariant: a renamed loser would be
// advertised under a name that existing name-scoped forbid policies do not cover.
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
	slog.Debug("resolving conflicts using priority strategy", "order", r.PriorityOrder)

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
				OutputSchema:              candidate.Tool.OutputSchema,
				Annotations:               candidate.Tool.Annotations,
				BackendID:                 candidate.BackendID,
				ConflictResolutionApplied: vmcp.ConflictStrategyPriority,
			}
			continue
		}

		if r.hasUnlistedCandidate(candidates) {
			// A collision involving a backend outside priorityOrder cannot be safely
			// rank-compared. Drop every candidate instead of awarding the bare name
			// to a listed backend or re-advertising losers under names outside
			// existing name-scoped policies.
			backendIDs := make([]string, len(candidates))
			for i, c := range candidates {
				backendIDs[i] = c.BackendID
			}
			slog.Error("dropped tool conflict involving backend not in priority order",
				"tool", toolName, "backends", backendIDs)

			droppedTools += len(candidates)
			continue
		}

		// Conflict detected among only listed backends; choose the highest priority backend.
		winner := r.selectWinner(candidates)
		resolved[toolName] = &ResolvedTool{
			ResolvedName:              toolName,
			OriginalName:              toolName,
			Description:               winner.Tool.Description,
			InputSchema:               winner.Tool.InputSchema,
			OutputSchema:              winner.Tool.OutputSchema,
			Annotations:               winner.Tool.Annotations,
			BackendID:                 winner.BackendID,
			ConflictResolutionApplied: vmcp.ConflictStrategyPriority,
		}

		// Log dropped tools
		for _, candidate := range candidates {
			if candidate.BackendID != winner.BackendID {
				slog.Warn("dropped tool from backend (lower priority)",
					"tool", toolName, "backend", candidate.BackendID, "winner", winner.BackendID)
				droppedTools++
			}
		}
	}

	if droppedTools > 0 {
		slog.Info("priority strategy resolved tools",
			"count", len(resolved), "dropped", droppedTools)
	} else {
		slog.Info("priority strategy resolved tools", "count", len(resolved))
	}

	return resolved, nil
}

func (r *PriorityConflictResolver) hasUnlistedCandidate(candidates []toolWithBackend) bool {
	for _, candidate := range candidates {
		if _, exists := r.priorityMap[candidate.BackendID]; !exists {
			return true
		}
	}
	return false
}

// selectWinner chooses the tool from the highest-priority backend.
// Callers should only pass candidates from backends that are in the priority list.
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
