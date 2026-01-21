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
			name:      "filter only specific tools",
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
			wantCount: 2,
			wantNames: []string{"create_pr", "merge_pr"},
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
			name:      "filter and override combined",
			backendID: "github",
			tools: []vmcp.Tool{
				{Name: "create_pr", Description: "Create PR", BackendID: "github"},
				{Name: "merge_pr", Description: "Merge PR", BackendID: "github"},
				{Name: "delete_pr", Description: "Delete PR", BackendID: "github"},
			},
			workloadConfig: &config.WorkloadToolConfig{
				Workload: "github",
				// Filter uses user-facing names (after override)
				Filter: []string{"gh_create_pr", "merge_pr"},
				Overrides: map[string]*config.ToolOverride{
					"create_pr": {Name: "gh_create_pr"},
				},
			},
			wantCount: 2,
			wantNames: []string{"gh_create_pr", "merge_pr"},
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
