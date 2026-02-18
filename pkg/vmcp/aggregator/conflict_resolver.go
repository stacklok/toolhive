// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package aggregator provides capability aggregation for Virtual MCP Server.
//
// This file contains the factory function for creating conflict resolvers
// and shared helper functions used by multiple resolver implementations.
package aggregator

import (
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// NewConflictResolver creates the appropriate conflict resolver based on configuration.
func NewConflictResolver(aggregationConfig *config.AggregationConfig) (ConflictResolver, error) {
	if aggregationConfig == nil {
		// Default to prefix strategy with default format
		slog.Info("No aggregation config provided, using default prefix strategy")
		return NewPrefixConflictResolver("{workload}_"), nil
	}

	switch aggregationConfig.ConflictResolution {
	case vmcp.ConflictStrategyPrefix:
		prefixFormat := "{workload}_" // Default
		if aggregationConfig.ConflictResolutionConfig != nil &&
			aggregationConfig.ConflictResolutionConfig.PrefixFormat != "" {
			prefixFormat = aggregationConfig.ConflictResolutionConfig.PrefixFormat
		}
		slog.Info("Using prefix conflict resolution strategy", "format", prefixFormat)
		return NewPrefixConflictResolver(prefixFormat), nil

	case vmcp.ConflictStrategyPriority:
		if aggregationConfig.ConflictResolutionConfig == nil ||
			len(aggregationConfig.ConflictResolutionConfig.PriorityOrder) == 0 {
			return nil, fmt.Errorf("priority strategy requires priority_order in conflict_resolution_config")
		}
		slog.Info("Using priority conflict resolution strategy", "order", aggregationConfig.ConflictResolutionConfig.PriorityOrder)
		return NewPriorityConflictResolver(aggregationConfig.ConflictResolutionConfig.PriorityOrder)

	case vmcp.ConflictStrategyManual:
		slog.Info("Using manual conflict resolution strategy")
		return NewManualConflictResolver(aggregationConfig.Tools)

	default:
		return nil, fmt.Errorf("%w: %s", ErrInvalidConflictStrategy, aggregationConfig.ConflictResolution)
	}
}

// toolWithBackend is a helper struct to track which backend a tool comes from.
// This is shared by multiple conflict resolution strategies.
type toolWithBackend struct {
	Tool      vmcp.Tool
	BackendID string
}

// groupToolsByName groups tools by their names to detect conflicts.
// This is shared by multiple conflict resolution strategies.
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
