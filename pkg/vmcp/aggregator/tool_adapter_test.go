// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"testing"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestProcessBackendTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		backendID      string
		tools          []vmcp.Tool
		workloadConfig *config.WorkloadToolConfig
		wantCount      int
		wantNames      []string
	}{
		{
			name:      "no configuration - all tools pass through",
			backendID: "github",
			tools: []vmcp.Tool{
				{Name: "create_pr", Description: "Create PR", InputSchema: map[string]any{"type": "object"}, BackendID: "github"},
				{Name: "merge_pr", Description: "Merge PR", InputSchema: map[string]any{"type": "object"}, BackendID: "github"},
			},
			workloadConfig: nil,
			wantCount:      2,
			wantNames:      []string{"create_pr", "merge_pr"},
		},
		{
			// NOTE: processBackendTools does NOT apply Filter - it's applied
			// later in MergeCapabilities (via shouldAdvertiseTool). This allows
			// the routing table to contain all tools for composite tools.
			name:      "filter is ignored by processBackendTools (applied in MergeCapabilities)",
			backendID: "github",
			tools: []vmcp.Tool{
				{Name: "create_pr", Description: "Create PR", BackendID: "github"},
				{Name: "merge_pr", Description: "Merge PR", BackendID: "github"},
				{Name: "list_prs", Description: "List PRs", BackendID: "github"},
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload: "github",
				Filter:   []string{"create_pr", "merge_pr"},
			},
			wantCount: 3, // All tools pass through - Filter is applied later in MergeCapabilities
			wantNames: []string{"create_pr", "merge_pr", "list_prs"},
		},
		{
			name:      "override tool names",
			backendID: "github",
			tools: []vmcp.Tool{
				{Name: "create_issue", Description: "Create issue", InputSchema: map[string]any{"type": "object"}, BackendID: "github"},
				{Name: "list_repos", Description: "List repos", BackendID: "github"},
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload: "github",
				Overrides: map[string]*config.ToolOverride{
					"create_issue": {Name: "gh_create_issue", Description: "Create GitHub issue"},
				},
			},
			wantCount: 2,
			wantNames: []string{"gh_create_issue", "list_repos"},
		},
		{
			// Filter is not applied here, but override is
			// All tools pass through with overrides applied
			name:      "filter ignored but override applied",
			backendID: "github",
			tools: []vmcp.Tool{
				{Name: "create_pr", Description: "Create PR", BackendID: "github"},
				{Name: "merge_pr", Description: "Merge PR", BackendID: "github"},
				{Name: "delete_pr", Description: "Delete PR", BackendID: "github"},
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload: "github",
				// Filter is ignored in processBackendTools (applied later)
				Filter: []string{"gh_create_pr", "merge_pr"},
				Overrides: map[string]*config.ToolOverride{
					"create_pr": {Name: "gh_create_pr"},
				},
			},
			wantCount: 3, // All tools pass through - Filter is applied later
			wantNames: []string{"gh_create_pr", "merge_pr", "delete_pr"},
		},
		{
			name:      "description override only",
			backendID: "github",
			tools: []vmcp.Tool{
				{Name: "create_pr", Description: "Original description", BackendID: "github"},
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload: "github",
				Overrides: map[string]*config.ToolOverride{
					"create_pr": {Description: "Updated description"},
				},
			},
			wantCount: 1,
			wantNames: []string{"create_pr"},
		},
		{
			name:      "preserves InputSchema and BackendID",
			backendID: "backend1",
			tools: []vmcp.Tool{
				{
					Name:        "tool1",
					Description: "Tool 1",
					InputSchema: map[string]any{"type": "object", "properties": map[string]any{"param": map[string]any{"type": "string"}}},
					BackendID:   "backend1",
				},
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload: "backend1",
				Overrides: map[string]*config.ToolOverride{
					"tool1": {Name: "renamed_tool1"},
				},
			},
			wantCount: 1,
			wantNames: []string{"renamed_tool1"},
		},
		{
			// NOTE: processBackendTools does NOT apply ExcludeAll - it's applied
			// later in MergeCapabilities. This allows the routing table to contain
			// all tools (for composite tools) while only filtering the advertised tools.
			name:      "excludeAll is ignored by processBackendTools (applied in MergeCapabilities)",
			backendID: "github",
			tools: []vmcp.Tool{
				{Name: "create_pr", Description: "Create PR", BackendID: "github"},
				{Name: "merge_pr", Description: "Merge PR", BackendID: "github"},
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload:   "github",
				ExcludeAll: true,
			},
			wantCount: 2, // All tools pass through - ExcludeAll is applied later
			wantNames: []string{"create_pr", "merge_pr"},
		},
		{
			// Both ExcludeAll and Filter are ignored here; applied in MergeCapabilities
			name:      "both excludeAll and filter are ignored by processBackendTools",
			backendID: "github",
			tools: []vmcp.Tool{
				{Name: "create_pr", Description: "Create PR", BackendID: "github"},
				{Name: "merge_pr", Description: "Merge PR", BackendID: "github"},
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload:   "github",
				ExcludeAll: true,
				Filter:     []string{"create_pr"},
			},
			wantCount: 2, // All tools pass through - both ExcludeAll and Filter applied later
			wantNames: []string{"create_pr", "merge_pr"},
		},
		{
			// ExcludeAll is ignored here; overrides are still applied
			name:      "excludeAll is ignored but overrides still apply",
			backendID: "github",
			tools: []vmcp.Tool{
				{Name: "create_pr", Description: "Create PR", BackendID: "github"},
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload:   "github",
				ExcludeAll: true,
				Overrides: map[string]*config.ToolOverride{
					"create_pr": {Name: "gh_create_pr"},
				},
			},
			wantCount: 1, // Override is applied, ExcludeAll is not
			wantNames: []string{"gh_create_pr"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := processBackendTools(context.Background(), tt.backendID, tt.tools, tt.workloadConfig)

			if len(result) != tt.wantCount {
				t.Errorf("got %d tools, want %d", len(result), tt.wantCount)
			}

			// Check expected tool names are present
			resultNames := make(map[string]bool)
			for _, tool := range result {
				resultNames[tool.Name] = true
			}

			for _, wantName := range tt.wantNames {
				if !resultNames[wantName] {
					t.Errorf("expected tool %q not found in results", wantName)
				}
			}

			// Verify InputSchema and BackendID are preserved
			for i, resultTool := range result {
				if resultTool.InputSchema != nil {
					// Find original tool to verify schema preservation
					for _, origTool := range tt.tools {
						if origTool.InputSchema != nil {
							// Schema should be preserved (same reference)
							if len(resultTool.InputSchema) == 0 && len(origTool.InputSchema) > 0 {
								t.Errorf("tool %d lost InputSchema", i)
							}
						}
					}
				}

				if resultTool.BackendID != tt.backendID {
					t.Errorf("tool %d has BackendID %q, want %q", i, resultTool.BackendID, tt.backendID)
				}
			}
		})
	}
}
