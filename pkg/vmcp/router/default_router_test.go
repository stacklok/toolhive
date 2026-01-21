// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package router_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
)

func TestDefaultRouter_RouteTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupTable    *vmcp.RoutingTable
		toolName      string
		expectedID    string
		expectError   bool
		errorContains string
	}{
		{
			name: "route to existing tool",
			setupTable: &vmcp.RoutingTable{
				Tools: map[string]*vmcp.BackendTarget{
					"test_tool": {
						WorkloadID:   "backend1",
						WorkloadName: "Backend 1",
						BaseURL:      "http://backend1:8080",
					},
				},
				Resources: make(map[string]*vmcp.BackendTarget),
				Prompts:   make(map[string]*vmcp.BackendTarget),
			},
			toolName:    "test_tool",
			expectedID:  "backend1",
			expectError: false,
		},
		{
			name: "tool not found",
			setupTable: &vmcp.RoutingTable{
				Tools:     make(map[string]*vmcp.BackendTarget),
				Resources: make(map[string]*vmcp.BackendTarget),
				Prompts:   make(map[string]*vmcp.BackendTarget),
			},
			toolName:      "nonexistent_tool",
			expectError:   true,
			errorContains: "tool not found",
		},
		{
			name:          "capabilities not in context",
			setupTable:    nil,
			toolName:      "test_tool",
			expectError:   true,
			errorContains: "capabilities not found in context",
		},
		{
			name: "routing table tools map is nil",
			setupTable: &vmcp.RoutingTable{
				Tools:     nil, // nil map
				Resources: make(map[string]*vmcp.BackendTarget),
				Prompts:   make(map[string]*vmcp.BackendTarget),
			},
			toolName:      "test_tool",
			expectError:   true,
			errorContains: "routing table tools map not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			r := router.NewDefaultRouter()

			// Setup routing table in context if provided
			if tt.setupTable != nil {
				caps := &aggregator.AggregatedCapabilities{
					RoutingTable: tt.setupTable,
				}
				ctx = discovery.WithDiscoveredCapabilities(ctx, caps)
			}

			// Test routing
			target, err := r.RouteTool(ctx, tt.toolName)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				assert.Nil(t, target)
			} else {
				require.NoError(t, err)
				require.NotNil(t, target)
				assert.Equal(t, tt.expectedID, target.WorkloadID)
			}
		})
	}
}

func TestDefaultRouter_RouteResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupTable    *vmcp.RoutingTable
		uri           string
		expectedID    string
		expectError   bool
		errorContains string
	}{
		{
			name: "route to existing resource",
			setupTable: &vmcp.RoutingTable{
				Tools: make(map[string]*vmcp.BackendTarget),
				Resources: map[string]*vmcp.BackendTarget{
					"file:///path/to/resource": {
						WorkloadID:   "backend2",
						WorkloadName: "Backend 2",
						BaseURL:      "http://backend2:8080",
					},
				},
				Prompts: make(map[string]*vmcp.BackendTarget),
			},
			uri:         "file:///path/to/resource",
			expectedID:  "backend2",
			expectError: false,
		},
		{
			name: "resource not found",
			setupTable: &vmcp.RoutingTable{
				Tools:     make(map[string]*vmcp.BackendTarget),
				Resources: make(map[string]*vmcp.BackendTarget),
				Prompts:   make(map[string]*vmcp.BackendTarget),
			},
			uri:           "file:///nonexistent",
			expectError:   true,
			errorContains: "resource not found",
		},
		{
			name:          "capabilities not in context",
			setupTable:    nil,
			uri:           "file:///test",
			expectError:   true,
			errorContains: "capabilities not found in context",
		},
		{
			name: "routing table resources map is nil",
			setupTable: &vmcp.RoutingTable{
				Tools:     make(map[string]*vmcp.BackendTarget),
				Resources: nil, // nil map
				Prompts:   make(map[string]*vmcp.BackendTarget),
			},
			uri:           "file:///test",
			expectError:   true,
			errorContains: "routing table resources map not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			r := router.NewDefaultRouter()

			// Setup routing table in context if provided
			if tt.setupTable != nil {
				caps := &aggregator.AggregatedCapabilities{
					RoutingTable: tt.setupTable,
				}
				ctx = discovery.WithDiscoveredCapabilities(ctx, caps)
			}

			// Test routing
			target, err := r.RouteResource(ctx, tt.uri)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				assert.Nil(t, target)
			} else {
				require.NoError(t, err)
				require.NotNil(t, target)
				assert.Equal(t, tt.expectedID, target.WorkloadID)
			}
		})
	}
}

func TestDefaultRouter_RoutePrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupTable    *vmcp.RoutingTable
		promptName    string
		expectedID    string
		expectError   bool
		errorContains string
	}{
		{
			name: "route to existing prompt",
			setupTable: &vmcp.RoutingTable{
				Tools:     make(map[string]*vmcp.BackendTarget),
				Resources: make(map[string]*vmcp.BackendTarget),
				Prompts: map[string]*vmcp.BackendTarget{
					"greeting": {
						WorkloadID:   "backend3",
						WorkloadName: "Backend 3",
						BaseURL:      "http://backend3:8080",
					},
				},
			},
			promptName:  "greeting",
			expectedID:  "backend3",
			expectError: false,
		},
		{
			name: "prompt not found",
			setupTable: &vmcp.RoutingTable{
				Tools:     make(map[string]*vmcp.BackendTarget),
				Resources: make(map[string]*vmcp.BackendTarget),
				Prompts:   make(map[string]*vmcp.BackendTarget),
			},
			promptName:    "nonexistent",
			expectError:   true,
			errorContains: "prompt not found",
		},
		{
			name:          "capabilities not in context",
			setupTable:    nil,
			promptName:    "test",
			expectError:   true,
			errorContains: "capabilities not found in context",
		},
		{
			name: "routing table prompts map is nil",
			setupTable: &vmcp.RoutingTable{
				Tools:     make(map[string]*vmcp.BackendTarget),
				Resources: make(map[string]*vmcp.BackendTarget),
				Prompts:   nil, // nil map
			},
			promptName:    "test",
			expectError:   true,
			errorContains: "routing table prompts map not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			r := router.NewDefaultRouter()

			// Setup routing table in context if provided
			if tt.setupTable != nil {
				caps := &aggregator.AggregatedCapabilities{
					RoutingTable: tt.setupTable,
				}
				ctx = discovery.WithDiscoveredCapabilities(ctx, caps)
			}

			// Test routing
			target, err := r.RoutePrompt(ctx, tt.promptName)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				assert.Nil(t, target)
			} else {
				require.NoError(t, err)
				require.NotNil(t, target)
				assert.Equal(t, tt.expectedID, target.WorkloadID)
			}
		})
	}
}

func TestDefaultRouter_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	// Setup routing table
	table := &vmcp.RoutingTable{
		Tools: map[string]*vmcp.BackendTarget{
			"tool1": {WorkloadID: "backend1"},
			"tool2": {WorkloadID: "backend2"},
		},
		Resources: map[string]*vmcp.BackendTarget{
			"res1": {WorkloadID: "backend1"},
		},
		Prompts: map[string]*vmcp.BackendTarget{
			"prompt1": {WorkloadID: "backend2"},
		},
	}

	caps := &aggregator.AggregatedCapabilities{
		RoutingTable: table,
	}
	ctx := discovery.WithDiscoveredCapabilities(context.Background(), caps)

	r := router.NewDefaultRouter()

	// Run concurrent readers - router is stateless so this should be safe
	const numGoroutines = 10
	const numOperations = 100

	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			for j := 0; j < numOperations; j++ {
				_, _ = r.RouteTool(ctx, "tool1")
				_, _ = r.RouteResource(ctx, "res1")
				_, _ = r.RoutePrompt(ctx, "prompt1")
			}
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify router still works correctly
	target, err := r.RouteTool(ctx, "tool1")
	require.NoError(t, err)
	assert.Equal(t, "backend1", target.WorkloadID)
}
