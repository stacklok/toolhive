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
	"strings"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// defaultPrefixFormat is the prefix format applied when none is configured.
const defaultPrefixFormat = "{workload}_"

// applyPrefixFormat expands the {workload} placeholder in prefixFormat with
// backendID and prepends the result to name. Shared by tool prefixing
// (PrefixConflictResolver) and unconditional prompt prefixing
// (resolvePromptConflicts) so both compose advertised names identically.
func applyPrefixFormat(prefixFormat, backendID, name string) string {
	return strings.ReplaceAll(prefixFormat, "{workload}", backendID) + name
}

// promptPrefixFormatFromConfig returns the prefix format prompts are renamed
// with: conflictResolutionConfig.prefixFormat when set, defaultPrefixFormat
// otherwise. Prompts are always prefixed (the strategy knob selects a TOOL
// strategy only), so the format is honoured under every strategy.
func promptPrefixFormatFromConfig(aggregationConfig *config.AggregationConfig) string {
	if aggregationConfig != nil && aggregationConfig.ConflictResolutionConfig != nil &&
		aggregationConfig.ConflictResolutionConfig.PrefixFormat != "" {
		return aggregationConfig.ConflictResolutionConfig.PrefixFormat
	}
	return defaultPrefixFormat
}

// NewConflictResolver creates the appropriate conflict resolver based on configuration.
func NewConflictResolver(aggregationConfig *config.AggregationConfig) (ConflictResolver, error) {
	if aggregationConfig == nil {
		// Default to prefix strategy with default format
		slog.Info("no aggregation config provided, using default prefix strategy")
		return NewPrefixConflictResolver(defaultPrefixFormat), nil
	}

	switch aggregationConfig.ConflictResolution {
	case vmcp.ConflictStrategyPrefix:
		prefixFormat := defaultPrefixFormat
		if aggregationConfig.ConflictResolutionConfig != nil &&
			aggregationConfig.ConflictResolutionConfig.PrefixFormat != "" {
			prefixFormat = aggregationConfig.ConflictResolutionConfig.PrefixFormat
		}
		slog.Info("using prefix conflict resolution strategy", "format", prefixFormat)
		return NewPrefixConflictResolver(prefixFormat), nil

	case vmcp.ConflictStrategyPriority:
		if aggregationConfig.ConflictResolutionConfig == nil ||
			len(aggregationConfig.ConflictResolutionConfig.PriorityOrder) == 0 {
			return nil, fmt.Errorf("priority strategy requires priority_order in conflict_resolution_config")
		}
		slog.Info("using priority conflict resolution strategy", "order", aggregationConfig.ConflictResolutionConfig.PriorityOrder)
		return NewPriorityConflictResolver(aggregationConfig.ConflictResolutionConfig.PriorityOrder)

	case vmcp.ConflictStrategyManual:
		slog.Info("using manual conflict resolution strategy")
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
