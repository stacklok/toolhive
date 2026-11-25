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

// promptNaming captures how advertised prompt names are formed by
// resolvePromptConflicts. prefixFormat is the format backend-prefixed names
// use. priorityRank maps the backend IDs listed in
// conflictResolutionConfig.priorityOrder to their rank (lower wins); listed
// backends keep their bare prompt names. It is non-nil only under the
// priority strategy — nil means every prompt is prefixed.
type promptNaming struct {
	prefixFormat string
	priorityRank map[string]int
}

// promptNamingFromConfig derives prompt naming from the aggregation config.
// Default (prefix or manual strategy, or no config): every prompt is
// prefixed with prefixFormat (conflictResolutionConfig.prefixFormat when
// set), so the advertised name is a pure function of (backendID, name).
// Under the priority strategy, backends listed in priorityOrder keep their
// bare prompt names while unlisted backends stay ALWAYS prefixed — stricter
// than the tool priority resolver, which lets a conflict-free unlisted tool
// keep its bare name. Prompts cannot afford that laxity: their advertised
// name is an authorization identity (see capability_conflicts.go), so the
// only membership-independent choices are "bare because listed" and
// "prefixed because not".
func promptNamingFromConfig(aggregationConfig *config.AggregationConfig) promptNaming {
	naming := promptNaming{prefixFormat: defaultPrefixFormat}
	if aggregationConfig == nil {
		return naming
	}
	if aggregationConfig.ConflictResolutionConfig != nil &&
		aggregationConfig.ConflictResolutionConfig.PrefixFormat != "" {
		naming.prefixFormat = aggregationConfig.ConflictResolutionConfig.PrefixFormat
	}
	if aggregationConfig.ConflictResolution == vmcp.ConflictStrategyPriority &&
		aggregationConfig.ConflictResolutionConfig != nil {
		priorityOrder := aggregationConfig.ConflictResolutionConfig.PriorityOrder
		if len(priorityOrder) > 0 {
			naming.priorityRank = make(map[string]int, len(priorityOrder))
			for rank, backendID := range priorityOrder {
				// A backend duplicated in priorityOrder keeps its first
				// (highest-priority) position.
				if _, seen := naming.priorityRank[backendID]; !seen {
					naming.priorityRank[backendID] = rank
				}
			}
		}
	}
	return naming
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
