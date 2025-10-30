package router_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
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
			name:          "routing table not initialized",
			setupTable:    nil,
			toolName:      "test_tool",
			expectError:   true,
			errorContains: "routing table not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			r := router.NewDefaultRouter()

			// Setup routing table if provided
			if tt.setupTable != nil {
				err := r.UpdateRoutingTable(ctx, tt.setupTable)
				require.NoError(t, err)
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
			name:          "routing table not initialized",
			setupTable:    nil,
			uri:           "file:///test",
			expectError:   true,
			errorContains: "routing table not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			r := router.NewDefaultRouter()

			// Setup routing table if provided
			if tt.setupTable != nil {
				err := r.UpdateRoutingTable(ctx, tt.setupTable)
				require.NoError(t, err)
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
			name:          "routing table not initialized",
			setupTable:    nil,
			promptName:    "test",
			expectError:   true,
			errorContains: "routing table not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			r := router.NewDefaultRouter()

			// Setup routing table if provided
			if tt.setupTable != nil {
				err := r.UpdateRoutingTable(ctx, tt.setupTable)
				require.NoError(t, err)
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

func TestDefaultRouter_UpdateRoutingTable(t *testing.T) {
	t.Parallel()

	t.Run("successful update", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r := router.NewDefaultRouter()

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

		err := r.UpdateRoutingTable(ctx, table)
		require.NoError(t, err)

		// Verify tools can be routed
		target, err := r.RouteTool(ctx, "tool1")
		require.NoError(t, err)
		assert.Equal(t, "backend1", target.WorkloadID)

		target, err = r.RouteTool(ctx, "tool2")
		require.NoError(t, err)
		assert.Equal(t, "backend2", target.WorkloadID)

		// Verify resources can be routed
		target, err = r.RouteResource(ctx, "res1")
		require.NoError(t, err)
		assert.Equal(t, "backend1", target.WorkloadID)

		// Verify prompts can be routed
		target, err = r.RoutePrompt(ctx, "prompt1")
		require.NoError(t, err)
		assert.Equal(t, "backend2", target.WorkloadID)
	})

	t.Run("update with nil table", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r := router.NewDefaultRouter()

		err := r.UpdateRoutingTable(ctx, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "routing table cannot be nil")
	})

	t.Run("atomic update - old table remains until update completes", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r := router.NewDefaultRouter()

		// Setup initial table
		oldTable := &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"old_tool": {WorkloadID: "backend_old"},
			},
			Resources: make(map[string]*vmcp.BackendTarget),
			Prompts:   make(map[string]*vmcp.BackendTarget),
		}
		err := r.UpdateRoutingTable(ctx, oldTable)
		require.NoError(t, err)

		// Verify old tool is routable
		target, err := r.RouteTool(ctx, "old_tool")
		require.NoError(t, err)
		assert.Equal(t, "backend_old", target.WorkloadID)

		// Update to new table
		newTable := &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"new_tool": {WorkloadID: "backend_new"},
			},
			Resources: make(map[string]*vmcp.BackendTarget),
			Prompts:   make(map[string]*vmcp.BackendTarget),
		}
		err = r.UpdateRoutingTable(ctx, newTable)
		require.NoError(t, err)

		// Old tool should no longer be routable
		_, err = r.RouteTool(ctx, "old_tool")
		require.Error(t, err)

		// New tool should be routable
		target, err = r.RouteTool(ctx, "new_tool")
		require.NoError(t, err)
		assert.Equal(t, "backend_new", target.WorkloadID)
	})
}

func TestDefaultRouter_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := router.NewDefaultRouter()

	// Setup initial routing table
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
	err := r.UpdateRoutingTable(ctx, table)
	require.NoError(t, err)

	// Run concurrent reads and writes
	const numGoroutines = 10
	const numOperations = 100

	done := make(chan bool, numGoroutines)

	// Concurrent readers
	for i := 0; i < numGoroutines/2; i++ {
		go func() {
			for j := 0; j < numOperations; j++ {
				_, _ = r.RouteTool(ctx, "tool1")
				_, _ = r.RouteResource(ctx, "res1")
				_, _ = r.RoutePrompt(ctx, "prompt1")
			}
			done <- true
		}()
	}

	// Concurrent updaters
	for i := 0; i < numGoroutines/2; i++ {
		go func(_ int) {
			for j := 0; j < numOperations; j++ {
				newTable := &vmcp.RoutingTable{
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
				_ = r.UpdateRoutingTable(ctx, newTable)
			}
			done <- true
		}(i)
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
