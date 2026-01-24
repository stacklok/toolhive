// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestPrefixConflictResolver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		prefixFormat   string
		toolsByBackend map[string][]vmcp.Tool
		wantCount      int
		checkNames     map[string]string // resolved name -> expected backend ID
	}{
		{
			name:         "default prefix format with conflicts",
			prefixFormat: "{workload}_",
			toolsByBackend: map[string][]vmcp.Tool{
				"github": {
					{Name: "create_issue", Description: "Create GitHub issue"},
					{Name: "list_issues", Description: "List GitHub issues"},
				},
				"jira": {
					{Name: "create_issue", Description: "Create Jira issue"},
					{Name: "list_projects", Description: "List Jira projects"},
				},
			},
			wantCount: 4,
			checkNames: map[string]string{
				"github_create_issue": "github",
				"github_list_issues":  "github",
				"jira_create_issue":   "jira",
				"jira_list_projects":  "jira",
			},
		},
		{
			name:         "dot separator prefix",
			prefixFormat: "{workload}.",
			toolsByBackend: map[string][]vmcp.Tool{
				"backend1": {
					{Name: "tool1", Description: "Tool 1"},
				},
				"backend2": {
					{Name: "tool1", Description: "Tool 1 from backend2"},
				},
			},
			wantCount: 2,
			checkNames: map[string]string{
				"backend1.tool1": "backend1",
				"backend2.tool1": "backend2",
			},
		},
		{
			name:         "no conflicts",
			prefixFormat: "{workload}_",
			toolsByBackend: map[string][]vmcp.Tool{
				"github": {
					{Name: "create_pr", Description: "Create PR"},
				},
				"jira": {
					{Name: "create_ticket", Description: "Create ticket"},
				},
			},
			wantCount: 2,
			checkNames: map[string]string{
				"github_create_pr":   "github",
				"jira_create_ticket": "jira",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolver := NewPrefixConflictResolver(tt.prefixFormat)
			resolved, err := resolver.ResolveToolConflicts(context.Background(), tt.toolsByBackend)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(resolved) != tt.wantCount {
				t.Errorf("got %d resolved tools, want %d", len(resolved), tt.wantCount)
			}

			for resolvedName, expectedBackendID := range tt.checkNames {
				tool, exists := resolved[resolvedName]
				if !exists {
					t.Errorf("expected tool %q not found in resolved tools", resolvedName)
					continue
				}

				if tool.BackendID != expectedBackendID {
					t.Errorf("tool %q has backend %q, want %q", resolvedName, tool.BackendID, expectedBackendID)
				}

				if tool.ConflictResolutionApplied != vmcp.ConflictStrategyPrefix {
					t.Errorf("tool %q has wrong strategy %q, want %q", resolvedName, tool.ConflictResolutionApplied, vmcp.ConflictStrategyPrefix)
				}
			}
		})
	}
}

func TestPriorityConflictResolver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		priorityOrder  []string
		toolsByBackend map[string][]vmcp.Tool
		wantCount      int
		wantWinners    map[string]string                          // tool name -> expected backend ID
		wantStrategies map[string]vmcp.ConflictResolutionStrategy // tool name -> expected strategy (optional)
		wantErr        bool
	}{
		{
			name:          "basic priority resolution",
			priorityOrder: []string{"github", "jira"},
			toolsByBackend: map[string][]vmcp.Tool{
				"github": {
					{Name: "create_issue", Description: "GitHub issue"},
					{Name: "list_repos", Description: "List repos"},
				},
				"jira": {
					{Name: "create_issue", Description: "Jira issue"},
					{Name: "list_projects", Description: "List projects"},
				},
			},
			wantCount: 3,
			wantWinners: map[string]string{
				"create_issue":  "github", // github wins
				"list_repos":    "github",
				"list_projects": "jira",
			},
		},
		{
			name:          "three-way conflict",
			priorityOrder: []string{"primary", "secondary", "tertiary"},
			toolsByBackend: map[string][]vmcp.Tool{
				"primary": {
					{Name: "shared_tool", Description: "Primary version"},
				},
				"secondary": {
					{Name: "shared_tool", Description: "Secondary version"},
				},
				"tertiary": {
					{Name: "shared_tool", Description: "Tertiary version"},
				},
			},
			wantCount: 1,
			wantWinners: map[string]string{
				"shared_tool": "primary",
			},
		},
		{
			name:          "backends not in priority list are skipped",
			priorityOrder: []string{"github"},
			toolsByBackend: map[string][]vmcp.Tool{
				"github": {
					{Name: "tool1", Description: "GitHub tool"},
				},
				"unknown_backend": {
					{Name: "tool2", Description: "Unknown tool"},
				},
			},
			wantCount: 2, // Both tools included (no conflict)
			wantWinners: map[string]string{
				"tool1": "github",
				"tool2": "unknown_backend",
			},
		},
		{
			name:          "backends not in priority with conflict use prefix fallback",
			priorityOrder: []string{"github"},
			toolsByBackend: map[string][]vmcp.Tool{
				"github": {
					{Name: "create_issue", Description: "GitHub issue"},
				},
				"slack": {
					{Name: "send_message", Description: "Slack message"},
				},
				"teams": {
					{Name: "send_message", Description: "Teams message"},
				},
			},
			wantCount: 3, // All tools included, conflicting ones prefixed
			wantWinners: map[string]string{
				"create_issue":       "github", // In priority list
				"slack_send_message": "slack",  // Not in priority, prefixed
				"teams_send_message": "teams",  // Not in priority, prefixed
			},
			wantStrategies: map[string]vmcp.ConflictResolutionStrategy{
				"create_issue":       vmcp.ConflictStrategyPriority, // Priority strategy used
				"slack_send_message": vmcp.ConflictStrategyPrefix,   // Prefix fallback used
				"teams_send_message": vmcp.ConflictStrategyPrefix,   // Prefix fallback used
			},
		},
		{
			name:          "empty priority order",
			priorityOrder: []string{},
			toolsByBackend: map[string][]vmcp.Tool{
				"github": {{Name: "tool1"}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolver, err := NewPriorityConflictResolver(tt.priorityOrder)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error creating resolver: %v", err)
			}

			resolved, err := resolver.ResolveToolConflicts(context.Background(), tt.toolsByBackend)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(resolved) != tt.wantCount {
				t.Errorf("got %d resolved tools, want %d", len(resolved), tt.wantCount)
			}

			for toolName, expectedBackendID := range tt.wantWinners {
				tool, exists := resolved[toolName]
				if !exists {
					t.Errorf("expected tool %q not found", toolName)
					continue
				}

				if tool.BackendID != expectedBackendID {
					t.Errorf("tool %q from %q, want %q", toolName, tool.BackendID, expectedBackendID)
				}

				// Check strategy if specified
				if tt.wantStrategies != nil {
					if expectedStrategy, hasExpectedStrategy := tt.wantStrategies[toolName]; hasExpectedStrategy {
						if tool.ConflictResolutionApplied != expectedStrategy {
							t.Errorf("tool %q has strategy %q, want %q", toolName, tool.ConflictResolutionApplied, expectedStrategy)
						}
					}
				} else {
					// Default: expect priority strategy
					if tool.ConflictResolutionApplied != vmcp.ConflictStrategyPriority {
						t.Errorf("tool %q has wrong strategy %q, want %q", toolName, tool.ConflictResolutionApplied, vmcp.ConflictStrategyPriority)
					}
				}
			}
		})
	}
}

func TestManualConflictResolver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		workloadConfigs []*config.WorkloadToolConfig
		toolsByBackend  map[string][]vmcp.Tool
		wantCount       int
		wantNames       []string // Expected resolved names
		wantErr         bool
		errContains     string
	}{
		{
			name: "all conflicts resolved with overrides",
			workloadConfigs: []*config.WorkloadToolConfig{
				{
					Workload: "github",
					Overrides: map[string]*config.ToolOverride{
						"create_issue": {Name: "gh_create_issue"},
					},
				},
				{
					Workload: "jira",
					Overrides: map[string]*config.ToolOverride{
						"create_issue": {Name: "jira_create_issue"},
					},
				},
			},
			toolsByBackend: map[string][]vmcp.Tool{
				"github": {{Name: "create_issue", Description: "GitHub"}},
				"jira":   {{Name: "create_issue", Description: "Jira"}},
			},
			wantCount: 2,
			wantNames: []string{"gh_create_issue", "jira_create_issue"},
		},
		{
			name: "unresolved conflict fails validation",
			workloadConfigs: []*config.WorkloadToolConfig{
				{
					Workload: "github",
					Overrides: map[string]*config.ToolOverride{
						"create_issue": {Name: "gh_create_issue"},
					},
				},
				// jira has no override for create_issue
				{
					Workload: "jira",
				},
			},
			toolsByBackend: map[string][]vmcp.Tool{
				"github": {{Name: "create_issue"}},
				"jira":   {{Name: "create_issue"}},
			},
			wantErr:     true,
			errContains: "unresolved tool name conflicts",
		},
		{
			name: "no conflicts - no overrides needed",
			workloadConfigs: []*config.WorkloadToolConfig{
				{Workload: "github"},
				{Workload: "jira"},
			},
			toolsByBackend: map[string][]vmcp.Tool{
				"github": {{Name: "create_pr"}},
				"jira":   {{Name: "create_ticket"}},
			},
			wantCount: 2,
			wantNames: []string{"create_pr", "create_ticket"},
		},
		{
			name: "override description only",
			workloadConfigs: []*config.WorkloadToolConfig{
				{
					Workload: "github",
					Overrides: map[string]*config.ToolOverride{
						"create_pr": {Description: "Updated description"},
					},
				},
			},
			toolsByBackend: map[string][]vmcp.Tool{
				"github": {{Name: "create_pr", Description: "Original"}},
			},
			wantCount: 1,
			wantNames: []string{"create_pr"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolver, err := NewManualConflictResolver(tt.workloadConfigs)
			if err != nil {
				t.Fatalf("unexpected error creating resolver: %v", err)
			}

			resolved, err := resolver.ResolveToolConflicts(context.Background(), tt.toolsByBackend)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(resolved) != tt.wantCount {
				t.Errorf("got %d resolved tools, want %d", len(resolved), tt.wantCount)
			}

			for _, name := range tt.wantNames {
				if _, exists := resolved[name]; !exists {
					t.Errorf("expected tool %q not found", name)
				}
			}
		})
	}
}

func TestNewConflictResolver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *config.AggregationConfig
		wantErr bool
	}{
		{
			name: "prefix strategy",
			config: &config.AggregationConfig{
				ConflictResolution: vmcp.ConflictStrategyPrefix,
				ConflictResolutionConfig: &config.ConflictResolutionConfig{
					PrefixFormat: "{workload}_",
				},
			},
		},
		{
			name: "priority strategy",
			config: &config.AggregationConfig{
				ConflictResolution: vmcp.ConflictStrategyPriority,
				ConflictResolutionConfig: &config.ConflictResolutionConfig{
					PriorityOrder: []string{"backend1", "backend2"},
				},
			},
		},
		{
			name: "manual strategy",
			config: &config.AggregationConfig{
				ConflictResolution: vmcp.ConflictStrategyManual,
				Tools: []*config.WorkloadToolConfig{
					{Workload: "github"},
				},
			},
		},
		{
			name: "priority without priority order fails",
			config: &config.AggregationConfig{
				ConflictResolution: vmcp.ConflictStrategyPriority,
			},
			wantErr: true,
		},
		{
			name:   "nil config defaults to prefix",
			config: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resolver, err := NewConflictResolver(tt.config)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if resolver == nil {
				t.Fatal("got nil resolver")
			}
		})
	}
}
