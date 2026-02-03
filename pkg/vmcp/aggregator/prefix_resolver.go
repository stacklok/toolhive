// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"strings"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
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

	for backendID, tools := range toolsByBackend {
		for _, tool := range tools {
			// Apply prefix to create resolved name
			resolvedName := r.applyPrefix(backendID, tool.Name)

			// Check if this resolved name is unique
			if existing, exists := resolved[resolvedName]; exists {
				// This should be extremely rare with prefixing, but handle it
				logger.Warnf("Collision after prefixing: %s from %s conflicts with %s from %s",
					resolvedName, backendID, existing.ResolvedName, existing.BackendID)
				continue
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

	logger.Infof("Prefix strategy created %d unique tools", len(resolved))

	return resolved, nil
}

// applyPrefix applies the configured prefix format to a tool name.
func (r *PrefixConflictResolver) applyPrefix(backendID, toolName string) string {
	prefix := r.PrefixFormat

	// Replace {workload} placeholder with actual backend ID
	prefix = strings.ReplaceAll(prefix, "{workload}", backendID)

	return prefix + toolName
}
