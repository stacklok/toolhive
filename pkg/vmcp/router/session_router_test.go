// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package router_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
)

func TestSessionRouter_RouteTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		routingTable  *vmcp.RoutingTable
		toolName      string
		expectedID    string
		expectError   bool
		errorContains string
	}{
		{
			name: "route to existing tool",
			routingTable: &vmcp.RoutingTable{
				Tools: map[string]*vmcp.BackendTarget{
					"test_tool": {
						WorkloadID:   "backend1",
						WorkloadName: "Backend 1",
						BaseURL:      "http://backend1:8080",
					},
				},
			},
			toolName:    "test_tool",
			expectedID:  "backend1",
			expectError: false,
		},
		{
			name: "tool not found",
			routingTable: &vmcp.RoutingTable{
				Tools: make(map[string]*vmcp.BackendTarget),
			},
			toolName:      "nonexistent_tool",
			expectError:   true,
			errorContains: "tool not found",
		},
		{
			name:          "nil routing table",
			routingTable:  nil,
			toolName:      "test_tool",
			expectError:   true,
			errorContains: "tool not found",
		},
		{
			name: "nil tools map",
			routingTable: &vmcp.RoutingTable{
				Tools: nil,
			},
			toolName:      "test_tool",
			expectError:   true,
			errorContains: "tool not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := router.NewSessionRouter(tt.routingTable)
			target, err := r.RouteTool(context.Background(), tt.toolName)

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

func TestSessionRouter_RouteResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		routingTable  *vmcp.RoutingTable
		uri           string
		expectedID    string
		expectError   bool
		errorContains string
	}{
		{
			name: "route to existing resource",
			routingTable: &vmcp.RoutingTable{
				Resources: map[string]*vmcp.BackendTarget{
					"file:///path/to/resource": {
						WorkloadID:   "backend2",
						WorkloadName: "Backend 2",
						BaseURL:      "http://backend2:8080",
					},
				},
			},
			uri:         "file:///path/to/resource",
			expectedID:  "backend2",
			expectError: false,
		},
		{
			name: "resource not found",
			routingTable: &vmcp.RoutingTable{
				Resources: make(map[string]*vmcp.BackendTarget),
			},
			uri:           "file:///nonexistent",
			expectError:   true,
			errorContains: "resource not found",
		},
		{
			name:          "nil routing table",
			routingTable:  nil,
			uri:           "file:///test",
			expectError:   true,
			errorContains: "resource not found",
		},
		{
			name: "nil resources map",
			routingTable: &vmcp.RoutingTable{
				Resources: nil,
			},
			uri:           "file:///test",
			expectError:   true,
			errorContains: "resource not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := router.NewSessionRouter(tt.routingTable)
			target, err := r.RouteResource(context.Background(), tt.uri)

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

func TestSessionRouter_RoutePrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		routingTable  *vmcp.RoutingTable
		promptName    string
		expectedID    string
		expectError   bool
		errorContains string
	}{
		{
			name: "route to existing prompt",
			routingTable: &vmcp.RoutingTable{
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
			routingTable: &vmcp.RoutingTable{
				Prompts: make(map[string]*vmcp.BackendTarget),
			},
			promptName:    "nonexistent",
			expectError:   true,
			errorContains: "prompt not found",
		},
		{
			name:          "nil routing table",
			routingTable:  nil,
			promptName:    "test",
			expectError:   true,
			errorContains: "prompt not found",
		},
		{
			name: "nil prompts map",
			routingTable: &vmcp.RoutingTable{
				Prompts: nil,
			},
			promptName:    "test",
			expectError:   true,
			errorContains: "prompt not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := router.NewSessionRouter(tt.routingTable)
			target, err := r.RoutePrompt(context.Background(), tt.promptName)

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

func TestSessionRouter_ConcurrentAccess(t *testing.T) {
	t.Parallel()

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

	r := router.NewSessionRouter(table)
	ctx := context.Background()

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

	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	target, err := r.RouteTool(ctx, "tool1")
	require.NoError(t, err)
	assert.Equal(t, "backend1", target.WorkloadID)
}
