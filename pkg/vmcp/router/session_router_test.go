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
		{
			// Composite workflow steps use "{workloadID}.{toolName}" where toolName
			// is the original backend capability name. With prefix conflict resolution
			// the routing table key is "{workloadID}_toolName", so an exact match
			// fails. The dot-convention fallback must resolve it correctly.
			name: "dot convention resolved via workload ID and original capability name",
			routingTable: &vmcp.RoutingTable{
				Tools: map[string]*vmcp.BackendTarget{
					"my-backend_echo": {
						WorkloadID:             "my-backend",
						WorkloadName:           "My Backend",
						BaseURL:                "http://my-backend:8080",
						OriginalCapabilityName: "echo",
					},
				},
			},
			toolName:    "my-backend.echo",
			expectedID:  "my-backend",
			expectError: false,
		},
		{
			name: "dot convention: workload not in session",
			routingTable: &vmcp.RoutingTable{
				Tools: map[string]*vmcp.BackendTarget{
					"other-backend_echo": {
						WorkloadID:             "other-backend",
						OriginalCapabilityName: "echo",
					},
				},
			},
			toolName:      "my-backend.echo",
			expectError:   true,
			errorContains: "tool not found",
		},
		{
			name: "dot convention: capability name mismatch",
			routingTable: &vmcp.RoutingTable{
				Tools: map[string]*vmcp.BackendTarget{
					"my-backend_echo": {
						WorkloadID:             "my-backend",
						OriginalCapabilityName: "echo",
					},
				},
			},
			toolName:      "my-backend.fetch",
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

func TestSessionRouter_ResolveToolName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		routingTable *vmcp.RoutingTable
		toolName     string
		expectedName string
	}{
		{
			name: "exact key returned unchanged",
			routingTable: &vmcp.RoutingTable{
				Tools: map[string]*vmcp.BackendTarget{
					"my-backend_echo": {WorkloadID: "my-backend", OriginalCapabilityName: "echo"},
				},
			},
			toolName:     "my-backend_echo",
			expectedName: "my-backend_echo",
		},
		{
			name: "dot convention resolves to routing table key",
			routingTable: &vmcp.RoutingTable{
				Tools: map[string]*vmcp.BackendTarget{
					"my-backend_echo": {WorkloadID: "my-backend", OriginalCapabilityName: "echo"},
				},
			},
			toolName:     "my-backend.echo",
			expectedName: "my-backend_echo",
		},
		{
			name: "not found returns toolName unchanged (pass-through)",
			routingTable: &vmcp.RoutingTable{
				Tools: make(map[string]*vmcp.BackendTarget),
			},
			toolName:     "missing_tool",
			expectedName: "missing_tool",
		},
		{
			name:         "nil routing table returns toolName unchanged (pass-through)",
			routingTable: nil,
			toolName:     "any_tool",
			expectedName: "any_tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := router.NewSessionRouter(tt.routingTable)
			resolved := r.ResolveToolName(context.Background(), tt.toolName)

			assert.Equal(t, tt.expectedName, resolved)
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

// TestSessionRouter_RouteResource_TemplateFallback exercises the template-match
// fallback: when the exact-URI lookup misses, the URI is matched against the
// aggregated resource templates (RFC 6570), routing to the matching template's
// backend. It covers a single match, no match, and disjoint multiple templates
// where exactly one matches (first-match selection).
func TestSessionRouter_RouteResource_TemplateFallback(t *testing.T) {
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
			name: "URI matches a single template",
			routingTable: &vmcp.RoutingTable{
				Resources: map[string]*vmcp.BackendTarget{},
				ResourceTemplates: map[string]*vmcp.BackendTarget{
					"file:///logs/{date}.txt": {WorkloadID: "logs-backend"},
				},
			},
			uri:        "file:///logs/2025-01-01.txt",
			expectedID: "logs-backend",
		},
		{
			// Per the MCP spec, a completion/complete with a ref/resource carries
			// the URI TEMPLATE string itself, and a template does not match its
			// own template string. The exact template-string key must route.
			name: "exact template string routes to its backend",
			routingTable: &vmcp.RoutingTable{
				Resources: map[string]*vmcp.BackendTarget{},
				ResourceTemplates: map[string]*vmcp.BackendTarget{
					"file:///logs/{date}.txt": {WorkloadID: "logs-backend"},
				},
			},
			uri:        "file:///logs/{date}.txt",
			expectedID: "logs-backend",
		},
		{
			name: "exact resource wins over a matching template",
			routingTable: &vmcp.RoutingTable{
				Resources: map[string]*vmcp.BackendTarget{
					"file:///logs/2025-01-01.txt": {WorkloadID: "exact-backend"},
				},
				ResourceTemplates: map[string]*vmcp.BackendTarget{
					"file:///logs/{date}.txt": {WorkloadID: "template-backend"},
				},
			},
			uri:        "file:///logs/2025-01-01.txt",
			expectedID: "exact-backend",
		},
		{
			name: "URI matches no template",
			routingTable: &vmcp.RoutingTable{
				Resources: map[string]*vmcp.BackendTarget{},
				ResourceTemplates: map[string]*vmcp.BackendTarget{
					"file:///logs/{date}.txt": {WorkloadID: "logs-backend"},
				},
			},
			uri:           "db:///users/42",
			expectError:   true,
			errorContains: "resource not found",
		},
		{
			name: "disjoint multiple templates: only the matching one is selected",
			routingTable: &vmcp.RoutingTable{
				Resources: map[string]*vmcp.BackendTarget{},
				ResourceTemplates: map[string]*vmcp.BackendTarget{
					"file:///logs/{date}.txt": {WorkloadID: "logs-backend"},
					"db:///users/{id}":        {WorkloadID: "users-backend"},
				},
			},
			uri:        "db:///users/42",
			expectedID: "users-backend",
		},
		{
			name: "invalid template is skipped, valid one still matches",
			routingTable: &vmcp.RoutingTable{
				Resources: map[string]*vmcp.BackendTarget{},
				ResourceTemplates: map[string]*vmcp.BackendTarget{
					"file:///logs/{date}.txt": {WorkloadID: "logs-backend"},
					"://{bad":                 {WorkloadID: "broken-backend"},
				},
			},
			uri:        "file:///logs/2025-01-01.txt",
			expectedID: "logs-backend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := router.NewSessionRouter(tt.routingTable)
			target, err := r.RouteResource(t.Context(), tt.uri)

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

// TestSessionRouter_RouteResource_TemplateDeterminism locks in deterministic
// resolution when two OVERLAPPING templates match the same URI. Map iteration
// order is randomized per range, so before the sorted-key fix the winner could
// flip between runs; matchResourceTemplate now iterates template keys in sorted
// order, so the first-match winner is stable. "file:///{+path}" sorts before
// "file:///{a}/{b}" ('+' 0x2B < 'a' 0x61), so the greedy template wins every run.
func TestSessionRouter_RouteResource_TemplateDeterminism(t *testing.T) {
	t.Parallel()

	const uri = "file:///logs/foo"

	// A single shared map is sufficient: Go randomizes iteration order on every
	// range, so each RouteResource call below exercises a fresh order.
	rt := &vmcp.RoutingTable{
		Resources: map[string]*vmcp.BackendTarget{},
		ResourceTemplates: map[string]*vmcp.BackendTarget{
			"file:///{+path}": {WorkloadID: "greedy-backend"},
			"file:///{a}/{b}": {WorkloadID: "specific-backend"},
		},
	}

	for i := range 50 {
		r := router.NewSessionRouter(rt)
		target, err := r.RouteResource(t.Context(), uri)
		require.NoError(t, err)
		require.NotNil(t, target)
		assert.Equal(t, "greedy-backend", target.WorkloadID,
			"overlapping templates must resolve to the sorted-first key deterministically (run %d)", i)
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
